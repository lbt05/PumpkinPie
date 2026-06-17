package agentmgr

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc/peer"

	"github.com/pumpkinpie/pumpkinpie/internal/master/metrics"
	"github.com/pumpkinpie/pumpkinpie/internal/master/store"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

// Manager tracks all connected agents and routes outbound messages to them.
type Manager struct {
	store    *store.Store
	sink     metrics.Sink
	mu       sync.RWMutex
	sessions map[string]*Session // keyed by nodeID
	updates  chan NodeUpdate     // broadcast channel for subscribers
	// metrics fanout: handleMetrics pushes Metric values here, a
	// single worker drains and writes them to the sink. Buffered so
	// the gRPC handler loop never blocks on slow downstream sinks;
	// on overflow we drop (with a debug log) rather than backpressure
	// the agent — the SQLite snapshot is already authoritative.
	sinkCh   chan metrics.Metric
	sinkCtx  context.Context
	sinkStop context.CancelFunc
	sinkWG   sync.WaitGroup
}

type NodeUpdate struct {
	NodeID string
	Kind   string // metrics / state / container
	At     time.Time
	// for metrics
	Snapshot *store.Node
}

func NewManager(s *store.Store, sink metrics.Sink) *Manager {
	if sink == nil {
		sink = metrics.NoopSink{}
	}
	ctx, stop := context.WithCancel(context.Background())
	m := &Manager{
		store:    s,
		sink:     sink,
		sessions: make(map[string]*Session),
		updates:  make(chan NodeUpdate, 256),
		sinkCh:   make(chan metrics.Metric, 1024),
		sinkCtx:  ctx,
		sinkStop: stop,
	}
	m.sinkWG.Add(1)
	go m.sinkLoop()
	return m
}

// Shutdown stops the metrics sink worker and closes the underlying
// sink. Safe to call multiple times; subsequent calls are no-ops.
func (m *Manager) Shutdown() {
	m.sinkStop()
	m.sinkWG.Wait()
	_ = m.sink.Close()
}

// sinkLoop drains the sink channel and forwards each Metric to the
// sink. It serializes writes through a per-call timeout so a stuck
// downstream (e.g. GreptimeDB hung on a slow DNS lookup) can't pin
// the worker forever.
func (m *Manager) sinkLoop() {
	defer m.sinkWG.Done()
	for {
		select {
		case <-m.sinkCtx.Done():
			return
		case metric := <-m.sinkCh:
			ctx, cancel := context.WithTimeout(m.sinkCtx, 5*time.Second)
			if err := m.sink.Write(ctx, metric); err != nil {
				log.Printf("[metrics] write node=%s: %v", metric.NodeID, err)
			}
			cancel()
		}
	}
}

// enqueueMetric is a non-blocking send into the sink channel. Drops
// the metric silently if the channel is full — the SQLite snapshot
// already reflects the report, so a GreptimeDB outage that pauses
// ingestion never produces an unbounded backlog in the master.
func (m *Manager) enqueueMetric(metric metrics.Metric) {
	select {
	case m.sinkCh <- metric:
	default:
		log.Printf("[metrics] sink channel full, dropping metric for node=%s", metric.NodeID)
	}
}

func (m *Manager) Updates() <-chan NodeUpdate { return m.updates }

// OnAgentConnect is called when a new agent gRPC stream opens.
// It returns the assigned nodeID and the session through which the gRPC handler
// can send messages back to the agent.
func (m *Manager) OnAgentConnect(ctx context.Context, stream pb.AgentService_ConnectServer) (*Session, string, error) {
	// First message must be RegisterRequest.
	first, err := stream.Recv()
	if err != nil {
		return nil, "", fmt.Errorf("recv first msg: %w", err)
	}
	reg := first.GetRegister()
	if reg == nil {
		return nil, "", fmt.Errorf("first message must be RegisterRequest")
	}

	nodeID, err := m.resolveNodeID(ctx, reg)
	if err != nil {
		return nil, "", err
	}

	sess := newSession(nodeID, reg.Name, stream, remoteHostFromContext(ctx))
	m.mu.Lock()
	if old, ok := m.sessions[nodeID]; ok {
		old.close("superseded by new connection")
	}
	m.sessions[nodeID] = sess
	m.mu.Unlock()

	if err := m.store.UpdateNodeState(ctx, nodeID, "online"); err != nil {
		return nil, "", err
	}
	if err := stream.Send(&pb.MasterMessage{
		Payload: &pb.MasterMessage_RegisterResp{
			RegisterResp: &pb.RegisterResponse{
				NodeId:            nodeID,
				Ok:                true,
				MetricsIntervalSec: 5,
			},
		},
	}); err != nil {
		return nil, "", err
	}

	m.publish(NodeUpdate{NodeID: nodeID, Kind: "state", At: time.Now()})
	return sess, nodeID, nil
}

func (m *Manager) resolveNodeID(ctx context.Context, reg *pb.RegisterRequest) (string, error) {
	if reg.MachineId == "" {
		return "", fmt.Errorf("register: machine_id required")
	}

	// Existing node for this machine? Reuse its ID and refresh the
	// mutable fields (name/hostname/os/version) so renaming an agent
	// doesn't create a duplicate row.
	existing, err := m.store.GetNodeByMachineID(ctx, reg.MachineId)
	if err == nil {
		existing.Name = reg.Name
		existing.Hostname = reg.Hostname
		existing.OS = reg.Os
		existing.Arch = reg.Arch
		existing.AgentVersion = reg.AgentVersion
		existing.State = "online"
		existing.LastHeartbeat = time.Now().UTC()
		if err := m.store.UpsertNode(ctx, existing); err != nil {
			return "", err
		}
		return existing.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// Brand new node.
	id, err := randomID()
	if err != nil {
		return "", err
	}
	n := &store.Node{
		ID:            id,
		MachineID:     reg.MachineId,
		Name:          reg.Name,
		Hostname:      reg.Hostname,
		OS:            reg.Os,
		Arch:          reg.Arch,
		AgentVersion:  reg.AgentVersion,
		State:         "online",
		LastHeartbeat: time.Now().UTC(),
		RegisteredAt:  time.Now().UTC(),
	}
	if err := m.store.UpsertNode(ctx, n); err != nil {
		return "", err
	}
	return id, nil
}

func (m *Manager) HandleMessages(ctx context.Context, nodeID string, stream pb.AgentService_ConnectServer) error {
	sess := m.Get(nodeID)
	if sess == nil {
		return fmt.Errorf("session missing")
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			m.handleDisconnect(nodeID, err)
			return err
		}
		sess.handleIncoming(ctx, m, msg)
	}
}

func (m *Manager) handleDisconnect(nodeID string, err error) {
	m.mu.Lock()
	s, ok := m.sessions[nodeID]
	if ok {
		delete(m.sessions, nodeID)
	}
	m.mu.Unlock()
	if s != nil {
		s.close(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.store.UpdateNodeState(ctx, nodeID, "offline")
	m.publish(NodeUpdate{NodeID: nodeID, Kind: "state", At: time.Now()})

	// Defensive: any container left in a transient state on this node
	// (the master set starting/stopping but the agent died before acking)
	// would otherwise stay stuck forever. Mark them as error so the
	// user can see the situation and free their GPU reservations.
	m.revertTransientOnDisconnect(ctx, nodeID, err.Error())
}

// revertTransientOnDisconnect flips any starting/stopping container on
// the given node back to error and releases its GPU reservations. It
// is best-effort: a failure here would only leave the row stuck in its
// transient state until the next lifecycle event.
func (m *Manager) revertTransientOnDisconnect(ctx context.Context, nodeID, reason string) {
	all, err := m.store.ListContainers(ctx)
	if err != nil {
		return
	}
	status := "agent disconnected: " + reason
	for _, c := range all {
		if c.NodeID != nodeID {
			continue
		}
		if c.State != "starting" && c.State != "stopping" {
			continue
		}
		_ = m.store.UpdateContainerState(ctx, c.ID, c.DockerID, "error", status)
		_ = m.store.FreeGPUs(ctx, c.ID)
		m.publish(NodeUpdate{NodeID: nodeID, Kind: "container", At: time.Now()})
	}
}

func (m *Manager) Get(nodeID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[nodeID]
}

func (m *Manager) Online() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		out = append(out, id)
	}
	return out
}

func (m *Manager) publish(u NodeUpdate) {
	select {
	case m.updates <- u:
	default:
		// drop if backlog full
	}
}

// ----- Session -----

type Session struct {
	NodeID string
	// nodeName is the human-readable name the agent registered with,
	// captured at connect time so handleMetrics can tag sink writes
	// without an extra DB lookup per report.
	nodeName string
	// remoteHost is the IP / hostname the agent's gRPC connection
	// came from, used by the proxy to dial back to the agent's
	// published container ports. Captured at OnAgentConnect from
	// the gRPC peer info.
	remoteHost string
	stream     pb.AgentService_ConnectServer
	sendMu     sync.Mutex
	closed     chan struct{}
	once       sync.Once
}

func newSession(nodeID, nodeName string, stream pb.AgentService_ConnectServer, remoteHost string) *Session {
	return &Session{
		NodeID:     nodeID,
		nodeName:   nodeName,
		remoteHost: remoteHost,
		stream:     stream,
		closed:     make(chan struct{}),
	}
}

// RemoteHost returns the host part of the agent's gRPC peer address.
// Returns "" if the address was not available at connect time, in
// which case the proxy should fall back to the node's reported
// hostname.
func (s *Session) RemoteHost() string { return s.remoteHost }

func (s *Session) handleIncoming(ctx context.Context, mgr *Manager, msg *pb.AgentMessage) {
	switch p := msg.Payload.(type) {
	case *pb.AgentMessage_Heartbeat:
		_ = mgr.store.UpdateNodeState(ctx, s.NodeID, "online")
		mgr.publish(NodeUpdate{NodeID: s.NodeID, Kind: "state", At: time.Now()})
	case *pb.AgentMessage_Metrics:
		s.handleMetrics(ctx, mgr, p.Metrics)
	case *pb.AgentMessage_ContainerCreated:
		s.handleContainerCreated(ctx, mgr, p.ContainerCreated)
	case *pb.AgentMessage_ContainerStopped:
		s.handleContainerStopped(ctx, mgr, p.ContainerStopped)
	case *pb.AgentMessage_ContainerStarted:
		s.handleContainerStarted(ctx, mgr, p.ContainerStarted)
	case *pb.AgentMessage_ContainerStateChanged:
		s.handleContainerStateChanged(ctx, mgr, p.ContainerStateChanged)
	}
}

func (s *Session) handleMetrics(ctx context.Context, mgr *Manager, r *pb.MetricReport) {
	n := &store.Node{ID: s.NodeID}
	if r.Cpu != nil {
		n.CPUPercent = r.Cpu.UsagePercent
		n.CPUCores = r.Cpu.Cores
	}
	if r.Memory != nil {
		n.MemUsedBytes = r.Memory.UsedBytes
		n.MemTotalBytes = r.Memory.TotalBytes
	}
	if r.Load != nil {
		n.Load1 = r.Load.Load1
	}
	if r.Gpu != nil {
		n.GpuCount = r.Gpu.Count
		n.GpuMemUsed = r.Gpu.MemUsedBytes
		n.GpuMemTotal = r.Gpu.MemTotalBytes
		n.GpuUsageAvg = r.Gpu.UsagePercent
		// serialize full GPU JSON
		b, _ := jsonMarshalGpu(r.Gpu)
		n.GPUJSON = b
	}
	if len(r.Disks) > 0 {
		b, _ := jsonMarshalDisks(r.Disks)
		n.DiskJSON = b
	}
	if err := mgr.store.UpdateNodeMetrics(ctx, n); err == nil {
		mgr.publish(NodeUpdate{NodeID: s.NodeID, Kind: "metrics", At: time.Now(), Snapshot: n})
	}
	// Forward to the configured metrics sink (NoopSink when no
	// backend is configured). Done out-of-band via enqueueMetric so
	// a slow sink can't stall the gRPC receive loop.
	ts := time.UnixMilli(r.TsUnixMs)
	mgr.enqueueMetric(metricFromReport(s.NodeID, s.nodeName, n, r, ts))
}

// metricFromReport builds the sink-facing projection from the live
// MetricReport + the Node we just persisted. Kept as a free function
// so the metrics package never has to import agentmgr (and vice versa
// stays a one-way dependency).
func metricFromReport(nodeID, nodeName string, n *store.Node, r *pb.MetricReport, ts time.Time) metrics.Metric {
	m := metrics.Metric{
		NodeID:        nodeID,
		NodeName:      nodeName,
		CPUPercent:    n.CPUPercent,
		CPUCores:      n.CPUCores,
		MemUsedBytes:  n.MemUsedBytes,
		MemTotalBytes: n.MemTotalBytes,
		Load1:         n.Load1,
		GpuCount:      n.GpuCount,
		GpuUsageAvg:   n.GpuUsageAvg,
		GpuMemUsed:    n.GpuMemUsed,
		GpuMemTotal:   n.GpuMemTotal,
		Disks:         metrics.DecodeDisks(n.DiskJSON),
		TS:            ts,
	}
	if r != nil && r.Gpu != nil {
		for _, d := range r.Gpu.Devices {
			m.GpuDevices = append(m.GpuDevices, metrics.GPUSample{
				Index:         d.Index,
				Name:          d.Name,
				UUID:          d.Uuid,
				UsagePercent:  d.UsagePercent,
				MemUsedBytes:  d.MemUsedBytes,
				MemTotalBytes: d.MemTotalBytes,
			})
		}
	}
	if m.TS.IsZero() {
		m.TS = time.Now().UTC()
	}
	return m
}

func (s *Session) handleContainerCreated(ctx context.Context, mgr *Manager, c *pb.ContainerCreated) {
	// The agent's docker.Create does create+start in one shot
	// (internal/agent/docker/client.go:179-181), so a successful ack
	// means the container is already running. Treat it as such and
	// collapse the old "created" intermediate state.
	state, status := "running", "running"
	if c.Error != "" {
		state, status = "error", c.Error
		// Release any GPU reservation we made on the master side — the
		// container never came up so those devices should be available
		// to other workloads.
		_ = mgr.store.FreeGPUs(ctx, c.ContainerId)
	}
	_ = mgr.store.UpdateContainerState(ctx, c.ContainerId, c.DockerId, state, status)
	mgr.publish(NodeUpdate{NodeID: s.NodeID, Kind: "container", At: time.Now()})
}

func (s *Session) handleContainerStopped(ctx context.Context, mgr *Manager, c *pb.ContainerStopped) {
	if c.Error != "" {
		_ = mgr.store.UpdateContainerState(ctx, c.ContainerId, c.DockerId, "error", c.Error)
	} else {
		_ = mgr.store.UpdateContainerState(ctx, c.ContainerId, c.DockerId, "exited", "exited")
	}
	mgr.publish(NodeUpdate{NodeID: s.NodeID, Kind: "container", At: time.Now()})
}

func (s *Session) handleContainerStarted(ctx context.Context, mgr *Manager, c *pb.ContainerStarted) {
	state, status := "running", "running"
	if c.Error != "" {
		state, status = "error", c.Error
	}
	_ = mgr.store.UpdateContainerState(ctx, c.ContainerId, c.DockerId, state, status)
	mgr.publish(NodeUpdate{NodeID: s.NodeID, Kind: "container", At: time.Now()})
}

// handleContainerStateChanged reconciles the master's stored row with
// a lifecycle event the agent observed directly from Docker — either
// via the /events stream or via the periodic /containers/json poll
// fallback. Triggered by out-of-band changes the master didn't
// initiate: user ran `docker stop` on the host, container OOM-killed,
// daemon restart, `docker rm`, ...
//
// Idempotency: Docker replays the boundary event on agent reconnect,
// and the periodic poll may push the same state twice. The state
// vocabulary is monotonic for terminal transitions (stop/die/kill/
// destroy all map to "exited"), and the agent's state-cache avoids
// emitting duplicate "poll" updates — replays converge to the same
// final state without an explicit event-id dedup table.
//
// Side effects on terminal transitions (action=destroy): we release
// the GPU reservation the master held for this container. Without
// this the GPU stays pinned until manual cleanup.
func (s *Session) handleContainerStateChanged(ctx context.Context, mgr *Manager, c *pb.ContainerStateChanged) {
	if c.ContainerId == "" || c.State == "" {
		return
	}
	exists, err := mgr.store.ContainerExists(ctx, c.ContainerId)
	if err != nil || !exists {
		// Orphan event for a container the master never knew about
		// (e.g. the user created it directly on the host with our
		// label by accident). Ignore — we have no row to update.
		return
	}
	_ = mgr.store.UpdateContainerState(ctx, c.ContainerId, c.DockerId, c.State, c.Status)
	if c.Action == "destroy" {
		_ = mgr.store.FreeGPUs(ctx, c.ContainerId)
	}
	mgr.publish(NodeUpdate{NodeID: s.NodeID, Kind: "container", At: time.Now()})
}

func (s *Session) close(reason string) {
	s.once.Do(func() {
		close(s.closed)
	})
}

func (s *Session) Closed() <-chan struct{} { return s.closed }

// Send writes a MasterMessage to the agent's stream thread-safely.
func (s *Session) Send(msg *pb.MasterMessage) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(msg)
}

// ---- utils ----

func remoteHostFromContext(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil || p.Addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		// Some transports return an addr without a port; use as-is.
		return p.Addr.String()
	}
	return host
}

func randomID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// light-weight JSON helpers (avoid pulling encoding/json into a hot path explicitly)
func jsonMarshalGpu(g *pb.GpuStats) (string, error) {
	devs := make([]map[string]any, 0, len(g.Devices))
	for _, d := range g.Devices {
		devs = append(devs, map[string]any{
			"index":          d.Index,
			"name":           d.Name,
			"uuid":           d.Uuid,
			"usage_percent":  d.UsagePercent,
			"mem_used_bytes": d.MemUsedBytes,
			"mem_total_bytes": d.MemTotalBytes,
		})
	}
	return encodeJSON(devs)
}

func jsonMarshalDisks(ds []*pb.DiskStats) (string, error) {
	out := make([]map[string]any, 0, len(ds))
	for _, d := range ds {
		out = append(out, map[string]any{
			"path":          d.Path,
			"total_bytes":   d.TotalBytes,
			"used_bytes":    d.UsedBytes,
			"usage_percent": d.UsagePercent,
		})
	}
	return encodeJSON(out)
}
