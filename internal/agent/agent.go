package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/pumpkinpie/pumpkinpie/internal/agent/collector"
	"github.com/pumpkinpie/pumpkinpie/internal/agent/docker"
	"github.com/pumpkinpie/pumpkinpie/internal/buildinfo"
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
	}, nil
}

// Run blocks until ctx is cancelled, reconnecting on failure with backoff.
func (a *Agent) Run(ctx context.Context) error {
	defer a.docker.Close()
	hostname, osStr, arch, err := collector.HostInfo()
	if err != nil {
		return fmt.Errorf("host info: %w", err)
	}
	version := normalizeAgentVersion(buildinfo.Version)

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

// normalizeAgentVersion strips the leading "v" from a semver tag so
// the value stored in the master's `nodes.agent_version` column is
// the bare version ("1.2.3") rather than the git tag form ("v1.2.3").
// The web UI's "agent v{{ ... }}" template re-adds the prefix on
// display, keeping the API contract stable for any external scripts
// that query agent_version directly.
func normalizeAgentVersion(v string) string {
	return strings.TrimPrefix(v, "v")
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

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
