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
