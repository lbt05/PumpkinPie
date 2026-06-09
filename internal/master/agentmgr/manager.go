package agentmgr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/pumpkinpie/pumpkinpie/internal/master/store"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

// Manager tracks all connected agents and routes outbound messages to them.
type Manager struct {
	store    *store.Store
	mu       sync.RWMutex
	sessions map[string]*Session // keyed by nodeID
	updates  chan NodeUpdate     // broadcast channel for subscribers
}

type NodeUpdate struct {
	NodeID string
	Kind   string // metrics / state / container
	At     time.Time
	// for metrics
	Snapshot *store.Node
}

func NewManager(s *store.Store) *Manager {
	return &Manager{
		store:    s,
		sessions: make(map[string]*Session),
		updates:  make(chan NodeUpdate, 256),
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

	sess := newSession(nodeID, stream)
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
	// Try to find existing node by name+hostname.
	nodes, err := m.store.ListNodes(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if n.Name == reg.Name && n.Hostname == reg.Hostname {
			return n.ID, nil
		}
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	n := &store.Node{
		ID:           id,
		Name:         reg.Name,
		Hostname:     reg.Hostname,
		OS:           reg.Os,
		Arch:         reg.Arch,
		AgentVersion: reg.AgentVersion,
		State:        "online",
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
	stream pb.AgentService_ConnectServer
	sendMu sync.Mutex
	closed chan struct{}
	once   sync.Once

	// proxy data from agent -> master (per-tunnel buffers)
	proxyMu     sync.Mutex
	proxyBuffer map[string]chan *pb.ProxyTunnelData  // tunnel_id -> buffered data
	proxyClose  map[string]chan string              // tunnel_id -> close reason
}

func newSession(nodeID string, stream pb.AgentService_ConnectServer) *Session {
	return &Session{
		NodeID:      nodeID,
		stream:      stream,
		closed:      make(chan struct{}),
		proxyBuffer: make(map[string]chan *pb.ProxyTunnelData),
		proxyClose:  make(map[string]chan string),
	}
}

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
	case *pb.AgentMessage_ProxyData:
		s.deliverProxyData(p.ProxyData)
	case *pb.AgentMessage_ProxyClose:
		s.deliverProxyClose(p.ProxyClose)
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
}

func (s *Session) handleContainerCreated(ctx context.Context, mgr *Manager, c *pb.ContainerCreated) {
	state, status := "created", "created"
	if c.Error != "" {
		state, status = "error", c.Error
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

// ---- Proxy data delivery (from agent to local listener) ----

func (s *Session) RegisterProxy(tunnelID string, bufSize int) {
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	if _, ok := s.proxyBuffer[tunnelID]; !ok {
		s.proxyBuffer[tunnelID] = make(chan *pb.ProxyTunnelData, bufSize)
		s.proxyClose[tunnelID] = make(chan string, 1)
	}
}

func (s *Session) UnregisterProxy(tunnelID string) {
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	delete(s.proxyBuffer, tunnelID)
	delete(s.proxyClose, tunnelID)
}

func (s *Session) ProxyDataChan(tunnelID string) <-chan *pb.ProxyTunnelData {
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	if ch, ok := s.proxyBuffer[tunnelID]; ok {
		return ch
	}
	ch := make(chan *pb.ProxyTunnelData, 64)
	s.proxyBuffer[tunnelID] = ch
	return ch
}

func (s *Session) ProxyCloseChan(tunnelID string) <-chan string {
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	if ch, ok := s.proxyClose[tunnelID]; ok {
		return ch
	}
	ch := make(chan string, 1)
	s.proxyClose[tunnelID] = ch
	return ch
}

func (s *Session) deliverProxyData(d *pb.ProxyTunnelData) {
	s.proxyMu.Lock()
	ch, ok := s.proxyBuffer[d.TunnelId]
	s.proxyMu.Unlock()
	if !ok {
		s.ProxyDataChan(d.TunnelId) // create
		s.proxyMu.Lock()
		ch = s.proxyBuffer[d.TunnelId]
		s.proxyMu.Unlock()
	}
	select {
	case ch <- d:
	case <-s.closed:
	}
}

func (s *Session) deliverProxyClose(d *pb.ProxyTunnelClose) {
	s.proxyMu.Lock()
	ch, ok := s.proxyClose[d.TunnelId]
	s.proxyMu.Unlock()
	if !ok {
		s.ProxyCloseChan(d.TunnelId) // create
		s.proxyMu.Lock()
		ch = s.proxyClose[d.TunnelId]
		s.proxyMu.Unlock()
	}
	select {
	case ch <- d.Reason:
	default:
	}
}

// ---- utils ----

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
