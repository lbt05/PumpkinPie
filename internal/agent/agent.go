package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/pumpkinpie/pumpkinpie/internal/agent/collector"
	"github.com/pumpkinpie/pumpkinpie/internal/agent/docker"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

type Agent struct {
	masterAddr string
	nodeName   string
	machineID  string

	collector *collector.Collector
	docker    *docker.Client

	streamMu sync.Mutex
	stream   pb.AgentService_ConnectClient

	// active proxy tunnels
	tunnelMu sync.Mutex
	tunnels  map[string]*tunnel
}

type tunnel struct {
	cancel   context.CancelFunc
	dataCh   chan *pb.ProxyTunnelData
	closeCh  chan struct{}
	finished chan struct{}
}

func New(masterAddr, nodeName, machineID string) (*Agent, error) {
	dc, err := docker.New()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Agent{
		masterAddr: masterAddr,
		nodeName:   nodeName,
		machineID:  machineID,
		collector:  collector.New(nodeName),
		docker:     dc,
		tunnels:    make(map[string]*tunnel),
	}, nil
}

// Run blocks until ctx is cancelled, reconnecting on failure with backoff.
func (a *Agent) Run(ctx context.Context) error {
	defer a.docker.Close()
	hostname, osStr, arch, version, err := collector.HostInfo()
	if err != nil {
		return fmt.Errorf("host info: %w", err)
	}

	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := a.serve(ctx, hostname, osStr, arch, version)
		if errors.Is(err, context.Canceled) {
			return err
		}
		log.Printf("agent disconnected: %v (reconnect in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		} else {
			backoff = 5 * time.Second
		}
	}
}

func (a *Agent) serve(ctx context.Context, hostname, os, arch, version string) error {
	conn, err := grpc.NewClient(a.masterAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
	)
	if err != nil {
		return err
	}
	defer conn.Close()
	cli := pb.NewAgentServiceClient(conn)

	stream, err := cli.Connect(ctx)
	if err != nil {
		return err
	}
	a.streamMu.Lock()
	a.stream = stream
	a.streamMu.Unlock()

	// 1) send register
	if err := stream.Send(&pb.AgentMessage{
		Payload: &pb.AgentMessage_Register{
			Register: &pb.RegisterRequest{
				Name:         a.nodeName,
				Hostname:     hostname,
				Os:           os,
				Arch:         arch,
				AgentVersion: version,
				MachineId:    a.machineID,
			},
		},
	}); err != nil {
		return err
	}

	// 2) wait for register response
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	regResp := first.GetRegisterResp()
	if regResp == nil {
		return fmt.Errorf("expected RegisterResponse, got %T", first.Payload)
	}
	if !regResp.Ok {
		return fmt.Errorf("registration rejected: %s", regResp.Error)
	}
	log.Printf("registered as node %s (metrics interval %ds)", regResp.NodeId, regResp.MetricsIntervalSec)
	interval := time.Duration(regResp.MetricsIntervalSec) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}

	// 3) start metrics ticker
	metricsCtx, cancelMetrics := context.WithCancel(ctx)
	defer cancelMetrics()
	go a.metricsLoop(metricsCtx, interval)

	// 4) read master messages until disconnect
	for {
		msg, err := stream.Recv()
		if err != nil {
			a.closeAllTunnels()
			return err
		}
		a.handleMasterMessage(ctx, msg)
	}
}

func (a *Agent) send(msg *pb.AgentMessage) error {
	a.streamMu.Lock()
	s := a.stream
	a.streamMu.Unlock()
	if s == nil {
		return errors.New("no active stream")
	}
	return s.Send(msg)
}

func (a *Agent) metricsLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r := a.collector.Collect(ctx)
			if err := a.send(&pb.AgentMessage{
				Payload: &pb.AgentMessage_Metrics{Metrics: r},
			}); err != nil {
				log.Printf("send metric: %v", err)
				return
			}
		}
	}
}

func (a *Agent) handleMasterMessage(ctx context.Context, msg *pb.MasterMessage) {
	switch p := msg.Payload.(type) {
	case *pb.MasterMessage_CreateContainer:
		go a.handleCreate(ctx, p.CreateContainer)
	case *pb.MasterMessage_StopContainer:
		go a.handleStop(ctx, p.StopContainer)
	case *pb.MasterMessage_StartContainer:
		go a.handleStart(ctx, p.StartContainer)
	case *pb.MasterMessage_OpenTunnel:
		go a.handleOpenTunnel(p.OpenTunnel)
	case *pb.MasterMessage_ProxyData:
		a.handleProxyDataFromMaster(p.ProxyData)
	case *pb.MasterMessage_ProxyClose:
		a.handleProxyCloseFromMaster(p.ProxyClose)
	}
}

func (a *Agent) handleCreate(ctx context.Context, cmd *pb.CreateContainerCommand) {
	dockerID, err := a.docker.Create(ctx, cmd)
	resp := &pb.ContainerCreated{ContainerId: cmd.ContainerId, DockerId: dockerID}
	if err != nil {
		resp.Error = err.Error()
		log.Printf("create container %s failed: %v", cmd.ContainerId, err)
	} else {
		log.Printf("created container %s (docker %s) on %s", cmd.ContainerId, shortID(dockerID), a.nodeName)
	}
	_ = a.send(&pb.AgentMessage{Payload: &pb.AgentMessage_ContainerCreated{ContainerCreated: resp}})
}

func (a *Agent) handleStop(ctx context.Context, cmd *pb.StopContainerCommand) {
	err := a.docker.Stop(ctx, cmd.DockerId, cmd.Remove)
	resp := &pb.ContainerStopped{ContainerId: cmd.ContainerId, DockerId: cmd.DockerId}
	if err != nil {
		resp.Error = err.Error()
	}
	_ = a.send(&pb.AgentMessage{Payload: &pb.AgentMessage_ContainerStopped{ContainerStopped: resp}})
}

func (a *Agent) handleStart(ctx context.Context, cmd *pb.StartContainerCommand) {
	err := a.docker.Start(ctx, cmd.DockerId)
	resp := &pb.ContainerStarted{ContainerId: cmd.ContainerId, DockerId: cmd.DockerId}
	if err != nil {
		resp.Error = err.Error()
		log.Printf("start container %s failed: %v", cmd.ContainerId, err)
	} else {
		log.Printf("started container %s (docker %s) on %s", cmd.ContainerId, shortID(cmd.DockerId), a.nodeName)
	}
	_ = a.send(&pb.AgentMessage{Payload: &pb.AgentMessage_ContainerStarted{ContainerStarted: resp}})
}

// ---- Proxy tunnel ----

// handleOpenTunnel opens a TCP connection to the docker container's mapped host port
// and starts a goroutine that streams data both ways. The tunnel struct is
// registered synchronously so the very next ProxyData message — which can
// arrive in the same gRPC Recv batch — can find it.
func (a *Agent) handleOpenTunnel(cmd *pb.OpenTunnel) {
	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnel{
		cancel:   cancel,
		dataCh:   make(chan *pb.ProxyTunnelData, 32),
		closeCh:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	a.tunnelMu.Lock()
	a.tunnels[cmd.TunnelId] = t
	a.tunnelMu.Unlock()

	go a.runTunnel(ctx, cmd, t)
}

// runTunnel connects to the container, reads request bytes from dataCh (filled by
// handleProxyDataFromMaster) and writes them to the container. Reads from the
// container are sent back as ProxyTunnelData to the master.
func (a *Agent) runTunnel(ctx context.Context, cmd *pb.OpenTunnel, t *tunnel) {
	defer close(t.finished)
	defer a.removeTunnel(cmd.TunnelId)

	// Find docker container ID for this master container_id, then its mapped port.
	dockerID, hostPort, err := a.resolveContainerHostPort(cmd.ContainerId, uint16(cmd.ContainerPort))
	if err != nil {
		log.Printf("tunnel %s resolve: %v", cmd.TunnelId, err)
		_ = a.send(&pb.AgentMessage{
			Payload: &pb.AgentMessage_ProxyClose{ProxyClose: &pb.ProxyTunnelClose{
				TunnelId: cmd.TunnelId, Reason: "resolve: " + err.Error(),
			}},
		})
		return
	}
	addr := fmt.Sprintf("127.0.0.1:%d", hostPort)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		log.Printf("tunnel %s dial %s: %v", cmd.TunnelId, addr, err)
		_ = a.send(&pb.AgentMessage{
			Payload: &pb.AgentMessage_ProxyClose{ProxyClose: &pb.ProxyTunnelClose{
				TunnelId: cmd.TunnelId, Reason: "dial: " + err.Error(),
			}},
		})
		return
	}
	defer conn.Close()
	log.Printf("tunnel %s -> %s (container %s, port %d)", cmd.TunnelId, addr, shortID(dockerID), cmd.ContainerPort)

	// writer goroutine: read from t.dataCh and write to container
	go func() {
		for d := range t.dataCh {
			if _, err := conn.Write(d.Data); err != nil {
				return
			}
			if d.CloseAfter {
				_ = conn.(*net.TCPConn).CloseWrite()
			}
		}
	}()

	// reader: read from container and send back
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if err := a.send(&pb.AgentMessage{
				Payload: &pb.AgentMessage_ProxyData{ProxyData: &pb.ProxyTunnelData{
					TunnelId: cmd.TunnelId,
					Data:     chunk,
				}},
			}); err != nil {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("tunnel %s read: %v", cmd.TunnelId, err)
			}
			break
		}
	}
	_ = a.send(&pb.AgentMessage{
		Payload: &pb.AgentMessage_ProxyClose{ProxyClose: &pb.ProxyTunnelClose{
			TunnelId: cmd.TunnelId, Reason: "eof",
		}},
	})
}

func (a *Agent) resolveContainerHostPort(containerID string, containerPort uint16) (string, uint16, error) {
	// List all containers, find by label
	cs, err := a.docker.ListAgent(context.Background())
	if err != nil {
		return "", 0, err
	}
	for _, ci := range cs {
		if ci.ContainerId == containerID {
			hostPort, err := a.docker.MappedHostPort(context.Background(), ci.DockerId, containerPort)
			if err != nil {
				return ci.DockerId, 0, err
			}
			return ci.DockerId, hostPort, nil
		}
	}
	return "", 0, fmt.Errorf("container %s not found locally", containerID)
}

func (a *Agent) handleProxyDataFromMaster(d *pb.ProxyTunnelData) {
	// The matching OpenTunnel may not have been processed yet (the master
	// sends both in the same gRPC stream and the agent processes them on
	// the same Recv loop). Briefly wait for the tunnel to appear.
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.tunnelMu.Lock()
		t, ok := a.tunnels[d.TunnelId]
		a.tunnelMu.Unlock()
		if ok {
			select {
			case t.dataCh <- d:
			case <-t.finished:
			}
			return
		}
		if time.Now().After(deadline) {
			log.Printf("agent: handleProxyDataFromMaster tunnel=%s not found (timed out)", d.TunnelId)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (a *Agent) handleProxyCloseFromMaster(d *pb.ProxyTunnelClose) {
	a.removeTunnel(d.TunnelId)
}

func (a *Agent) removeTunnel(id string) {
	a.tunnelMu.Lock()
	t, ok := a.tunnels[id]
	if ok {
		delete(a.tunnels, id)
	}
	a.tunnelMu.Unlock()
	if ok {
		t.cancel()
		close(t.dataCh) // signal writer goroutine to exit
	}
}

func (a *Agent) closeAllTunnels() {
	a.tunnelMu.Lock()
	for id, t := range a.tunnels {
		t.cancel()
		close(t.dataCh)
		delete(a.tunnels, id)
	}
	a.tunnelMu.Unlock()
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
