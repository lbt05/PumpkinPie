package agentmgr

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pumpkinpie/pumpkinpie/internal/master/metrics"
	"github.com/pumpkinpie/pumpkinpie/internal/master/store"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := NewManager(st, metrics.NoopSink{})
	t.Cleanup(m.Shutdown)
	return m
}

func TestResolveNodeID_RenameKeepsSameID(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	reg1 := &pb.RegisterRequest{
		Name:      "node-A",
		Hostname:  "host1",
		Os:        "linux",
		Arch:      "amd64",
		MachineId: "abc-123",
	}
	id1, err := m.resolveNodeID(ctx, reg1)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	reg2 := &pb.RegisterRequest{
		Name:      "node-A-renamed",
		Hostname:  "host1",
		Os:        "linux",
		Arch:      "amd64",
		MachineId: "abc-123",
	}
	id2, err := m.resolveNodeID(ctx, reg2)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected same node id after rename, got %s -> %s", id1, id2)
	}

	nodes, err := m.store.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Name != "node-A-renamed" {
		t.Errorf("expected name updated to node-A-renamed, got %q", nodes[0].Name)
	}
}

func TestResolveNodeID_DifferentMachineIDIsNewNode(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	id1, err := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "n", Hostname: "h", MachineId: "machine-1",
	})
	if err != nil {
		t.Fatalf("resolve 1: %v", err)
	}
	id2, err := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "n", Hostname: "h", MachineId: "machine-2",
	})
	if err != nil {
		t.Fatalf("resolve 2: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("different machine_id should yield different node id, both = %s", id1)
	}
}

func TestResolveNodeID_RejectsEmptyMachineID(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	_, err := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "n", Hostname: "h", MachineId: "",
	})
	if err == nil {
		t.Fatal("expected error for empty machine_id")
	}
	nodes, err := m.store.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected no nodes created, got %d", len(nodes))
	}
}

// fakeSink captures every Metric pushed through it so we can assert
// the manager actually forwards reports to the configured sink.
type fakeSink struct {
	mu  sync.Mutex
	got []metrics.Metric
}

func (f *fakeSink) Write(_ context.Context, m metrics.Metric) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, m)
	return nil
}
func (f *fakeSink) Close() error { return nil }

func (f *fakeSink) snapshot() []metrics.Metric {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]metrics.Metric, len(f.got))
	copy(out, f.got)
	return out
}

func TestHandleMetrics_ForwardsToSink(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sink := &fakeSink{}
	m := NewManager(st, sink)
	t.Cleanup(m.Shutdown)

	ctx := context.Background()
	nodeID, err := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "node-A", Hostname: "h", MachineId: "machine-x",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	sess := newSession(nodeID, "node-A", nil, "")

	report := &pb.MetricReport{
		TsUnixMs: 1700000000000,
		Cpu:      &pb.CpuStats{UsagePercent: 42.5, Cores: 8},
		Memory:   &pb.MemoryStats{TotalBytes: 1024, UsedBytes: 256},
		Load:     &pb.LoadAvg{Load1: 1.25},
		Gpu: &pb.GpuStats{
			Count: 1, UsagePercent: 10,
			MemUsedBytes: 100, MemTotalBytes: 1000,
			Devices: []*pb.GpuDevice{
				{Index: 0, Name: "A100", Uuid: "gpu-0", UsagePercent: 10, MemUsedBytes: 100, MemTotalBytes: 1000},
			},
		},
		Disks: []*pb.DiskStats{
			{Path: "/", TotalBytes: 10000, UsedBytes: 5000, UsagePercent: 50},
		},
	}
	sess.handleMetrics(ctx, m, report)

	// The sink worker drains via a channel, so give it a moment.
	deadline := time.After(2 * time.Second)
	for {
		if len(sink.snapshot()) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("sink did not receive metric in time")
		case <-time.After(10 * time.Millisecond):
		}
	}

	got := sink.snapshot()[0]
	if got.NodeID != nodeID {
		t.Errorf("node id: got %q, want %q", got.NodeID, nodeID)
	}
	if got.NodeName != "node-A" {
		t.Errorf("node name: got %q, want %q", got.NodeName, "node-A")
	}
	if got.CPUPercent != 42.5 {
		t.Errorf("cpu_percent: got %v, want 42.5", got.CPUPercent)
	}
	if got.Load1 != 1.25 {
		t.Errorf("load1: got %v, want 1.25", got.Load1)
	}
	if got.MemUsedBytes != 256 || got.MemTotalBytes != 1024 {
		t.Errorf("mem: got used=%d total=%d, want 256/1024", got.MemUsedBytes, got.MemTotalBytes)
	}
	if got.GpuCount != 1 || len(got.GpuDevices) != 1 {
		t.Errorf("gpu: count=%d devices=%d, want 1/1", got.GpuCount, len(got.GpuDevices))
	}
	if got.GpuDevices[0].Name != "A100" {
		t.Errorf("gpu[0].name: got %q, want A100", got.GpuDevices[0].Name)
	}
	if len(got.Disks) != 1 || got.Disks[0].Path != "/" {
		t.Errorf("disks: got %+v, want one entry for /", got.Disks)
	}
	if got.TS.UnixMilli() != 1700000000000 {
		t.Errorf("ts: got %d, want 1700000000000", got.TS.UnixMilli())
	}
}

func TestHandleMetrics_SinkChannelFullDrops(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Blocking sink keeps the worker busy; the channel (cap 1024)
	// will eventually fill and the next enqueue must drop silently.
	blocking := make(chan struct{})
	sink := blockingSink{block: blocking}
	m := NewManager(st, &sink)
	t.Cleanup(m.Shutdown)

	ctx := context.Background()
	nodeID, err := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "n", Hostname: "h", MachineId: "machine-x",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	sess := newSession(nodeID, "n", nil, "")

	// Fill the channel + the one in-flight slot. We don't assert on
	// the exact count — only that pushing more doesn't deadlock the
	// test (which would be a regression to blocking enqueue).
	for i := 0; i < 2000; i++ {
		sess.handleMetrics(ctx, m, &pb.MetricReport{TsUnixMs: 1})
	}
	close(blocking)
}

// blockingSink holds Write forever so the sink worker goroutine
// stays busy. Used to exercise the overflow-drop path.
type blockingSink struct {
	block <-chan struct{}
}

func (b *blockingSink) Write(ctx context.Context, _ metrics.Metric) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.block:
		return nil
	}
}
func (b *blockingSink) Close() error { return nil }

// TestHandleContainerStateChanged_OutOfBandStop covers the original
// bug report: the user runs `docker stop <id>` directly on the agent
// host, Docker stops the container, and the master's stored state
// must flip to "exited" without the master ever issuing the stop
// itself. Prior to the events-stream + polling wiring, the master row
// stayed stuck at "running" until the next agent reconnect.
func TestHandleContainerStateChanged_OutOfBandStop(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	nodeID, err := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "n", Hostname: "h", MachineId: "machine-os-1",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := m.store.InsertContainer(ctx, &store.Container{
		ID:       "c1",
		NodeID:   nodeID,
		DockerID: "deadbeef",
		Image:    "nginx:latest",
		State:    "running",
		Status:   "running",
	}); err != nil {
		t.Fatalf("seed container: %v", err)
	}

	sess := newSession(nodeID, "n", nil, "")
	sess.handleContainerStateChanged(ctx, m, &pb.ContainerStateChanged{
		ContainerId: "c1",
		DockerId:    "deadbeef",
		Action:      "die",
		State:       "exited",
		Status:      "exited",
		TsUnixMs:    time.Now().UnixMilli(),
	})

	got, err := m.store.GetContainer(ctx, "c1")
	if err != nil {
		t.Fatalf("get container: %v", err)
	}
	if got.State != "exited" || got.Status != "exited" {
		t.Errorf("state=%q status=%q, want both \"exited\"", got.State, got.Status)
	}
}

// TestHandleContainerStateChanged_PollActionAccepted locks in that
// the same handler accepts the action="poll" variant emitted by
// containerPollLoop on platforms where /events doesn't work. Without
// this path, Docker Desktop for Mac users would never see out-of-band
// `docker stop` reflected in the master.
func TestHandleContainerStateChanged_PollActionAccepted(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	nodeID, _ := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "n", Hostname: "h", MachineId: "machine-poll-1",
	})
	_ = m.store.InsertContainer(ctx, &store.Container{
		ID: "c1", NodeID: nodeID, DockerID: "deadbeef",
		Image: "nginx:latest", State: "running", Status: "running",
	})

	sess := newSession(nodeID, "n", nil, "")
	sess.handleContainerStateChanged(ctx, m, &pb.ContainerStateChanged{
		ContainerId: "c1",
		DockerId:    "deadbeef",
		Action:      "poll",
		State:       "exited",
		Status:      "exited",
	})

	got, _ := m.store.GetContainer(ctx, "c1")
	if got.State != "exited" {
		t.Errorf("poll action should still update state, got %q", got.State)
	}
}

// TestHandleContainerStateChanged_DestroyFreesGPUs checks the
// terminal-transition side effect: when Docker destroys the
// container (out-of-band `docker rm`), the master releases any
// GPU reservation it still holds, otherwise the devices stay
// pinned until manual cleanup.
func TestHandleContainerStateChanged_DestroyFreesGPUs(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	nodeID, _ := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "n", Hostname: "h", MachineId: "machine-os-2",
	})
	_ = m.store.UpsertNode(ctx, &store.Node{
		ID: nodeID, MachineID: "machine-os-2", Name: "n",
		State: "online", GpuCount: 4,
	})
	if err := m.store.InsertContainer(ctx, &store.Container{
		ID: "c1", NodeID: nodeID, DockerID: "deadbeef",
		Image: "tf:latest", State: "running", Status: "running",
	}); err != nil {
		t.Fatalf("seed container: %v", err)
	}
	if _, err := m.store.AllocGPUs(ctx, nodeID, "c1", 2, 4); err != nil {
		t.Fatalf("AllocGPUs: %v", err)
	}

	sess := newSession(nodeID, "n", nil, "")
	sess.handleContainerStateChanged(ctx, m, &pb.ContainerStateChanged{
		ContainerId: "c1",
		DockerId:    "deadbeef",
		Action:      "destroy",
		State:       "exited",
		Status:      "exited",
	})

	idx, err := m.store.GetContainerGPUs(ctx, "c1")
	if err != nil {
		t.Fatalf("GetContainerGPUs: %v", err)
	}
	if len(idx) != 0 {
		t.Errorf("expected GPUs released after destroy, still hold %v", idx)
	}
}

// TestHandleContainerStateChanged_OrphanIgnored ensures we don't
// crash on an event for a container the master never knew about
// (e.g. someone labelled a manual container with our prefix).
func TestHandleContainerStateChanged_OrphanIgnored(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	nodeID, _ := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "n", Hostname: "h", MachineId: "machine-os-3",
	})
	sess := newSession(nodeID, "n", nil, "")

	sess.handleContainerStateChanged(ctx, m, &pb.ContainerStateChanged{
		ContainerId: "ghost",
		Action:      "die",
		State:       "exited",
		Status:      "exited",
	})

	exists, err := m.store.ContainerExists(ctx, "ghost")
	if err != nil {
		t.Fatalf("ContainerExists: %v", err)
	}
	if exists {
		t.Errorf("orphan event should not create a container row")
	}
}

// seedTerminalActionContainer creates a node with 4 GPUs, a running
// container, and a 2-GPU reservation. Returned by the terminal-action
// tests below; each subtest runs against its own fresh DB so a fixed
// MachineId is safe.
func seedTerminalActionContainer(t *testing.T, m *Manager) string {
	t.Helper()
	ctx := context.Background()
	nodeID, _ := m.resolveNodeID(ctx, &pb.RegisterRequest{
		Name: "n", Hostname: "h", MachineId: "machine-terminal-action",
	})
	if err := m.store.UpsertNode(ctx, &store.Node{
		ID: nodeID, MachineID: "machine-terminal-action",
		Name: "n", State: "online", GpuCount: 4,
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if err := m.store.InsertContainer(ctx, &store.Container{
		ID: "c1", NodeID: nodeID, DockerID: "deadbeef",
		Image: "tf:latest", State: "running", Status: "running",
	}); err != nil {
		t.Fatalf("InsertContainer: %v", err)
	}
	if _, err := m.store.AllocGPUs(ctx, nodeID, "c1", 2, 4); err != nil {
		t.Fatalf("AllocGPUs: %v", err)
	}
	return nodeID
}

func assertContainerHoldsNoGPUs(t *testing.T, m *Manager, containerID string) {
	t.Helper()
	idx, err := m.store.GetContainerGPUs(context.Background(), containerID)
	if err != nil {
		t.Fatalf("GetContainerGPUs: %v", err)
	}
	if len(idx) != 0 {
		t.Errorf("expected GPUs released, still hold %v", idx)
	}
}

// TestHandleContainerStateChanged_TerminalActionsFreeGPUs locks in
// that every docker event-stream action that means "container no
// longer holds its GPUs" releases the master's reservation. Without
// this, an OOM-killed or out-of-band docker stop blocks subsequent
// /start with "requested GPU index is already allocated".
func TestHandleContainerStateChanged_TerminalActionsFreeGPUs(t *testing.T) {
	cases := []struct {
		name   string
		action string
	}{
		{"die", "die"},
		{"stop", "stop"},
		{"kill", "kill"},
		{"destroy", "destroy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManager(t)
			nodeID := seedTerminalActionContainer(t, m)
			sess := newSession(nodeID, "n", nil, "")
			sess.handleContainerStateChanged(context.Background(), m, &pb.ContainerStateChanged{
				ContainerId: "c1",
				DockerId:    "deadbeef",
				Action:      tc.action,
				State:       "exited",
				Status:      "exited",
			})
			assertContainerHoldsNoGPUs(t, m, "c1")
		})
	}
}

// TestHandleContainerStarted_ErrorFreesGPUs covers the rare race
// where the agent acks StartContainer with an error: the master
// pre-reserved the GPU in startContainer; the error means the docker
// container is not running and never will be. We must release the
// reservation here, otherwise the next /start hits the same row and
// returns "requested GPU index is already allocated".
func TestHandleContainerStarted_ErrorFreesGPUs(t *testing.T) {
	m := newTestManager(t)
	nodeID := seedTerminalActionContainer(t, m)
	sess := newSession(nodeID, "n", nil, "")
	sess.handleContainerStarted(context.Background(), m, &pb.ContainerStarted{
		ContainerId: "c1",
		DockerId:    "deadbeef",
		Error:       "docker start: device busy",
	})
	assertContainerHoldsNoGPUs(t, m, "c1")

	cc, err := m.store.GetContainer(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetContainer: %v", err)
	}
	if cc.State != "error" {
		t.Errorf("state = %q, want %q", cc.State, "error")
	}
}
