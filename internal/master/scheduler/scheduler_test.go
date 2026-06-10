package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pumpkinpie/pumpkinpie/internal/master/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sched.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func upsertNode(t *testing.T, s *store.Store, n *store.Node) {
	t.Helper()
	n.LastHeartbeat = time.Now()
	n.RegisteredAt = time.Now()
	if err := s.UpsertNode(context.Background(), n); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	// UpsertNode doesn't write metric columns; populate via UpdateNodeMetrics
	// so gpu_count / cpu_cores / mem_total etc. are visible to ListNodes.
	if err := s.UpdateNodeMetrics(context.Background(), n); err != nil {
		t.Fatalf("UpdateNodeMetrics: %v", err)
	}
}

func insertContainer(t *testing.T, s *store.Store, id, nodeID string) {
	t.Helper()
	if err := s.InsertContainer(context.Background(), &store.Container{
		ID: id, NodeID: nodeID, Image: "x", State: "running", Status: "ok",
	}); err != nil {
		t.Fatalf("InsertContainer: %v", err)
	}
}

func TestSelect_RejectsWhenNoFreeGPUs(t *testing.T) {
	s := openStore(t)
	upsertNode(t, s, &store.Node{
		ID: "n1", MachineID: "m-n1", Name: "n1", State: "online", GpuCount: 2,
	})
	// Both GPUs already held.
	insertContainer(t, s, "occupant", "n1")
	if _, err := s.AllocGPUs(context.Background(), "n1", "occupant", 2, 2); err != nil {
		t.Fatalf("seed alloc: %v", err)
	}

	_, err := Select(context.Background(), s, ResourceRequest{GPUCount: 1})
	if !errors.Is(err, ErrNoNode) {
		t.Fatalf("expected ErrNoNode, got %v", err)
	}
	if !strings.Contains(err.Error(), "1 online") {
		t.Errorf("error missing online count: %v", err)
	}
	if !strings.Contains(err.Error(), ">=1 free GPUs") {
		t.Errorf("error missing free-GPU reason: %v", err)
	}
}

func TestSelect_PicksNodeWithFreeGPU(t *testing.T) {
	s := openStore(t)
	upsertNode(t, s, &store.Node{
		ID: "full", MachineID: "m-full", Name: "full", State: "online", GpuCount: 2,
	})
	upsertNode(t, s, &store.Node{
		ID: "free", MachineID: "m-free", Name: "free", State: "online", GpuCount: 2,
	})
	insertContainer(t, s, "occ", "full")
	if _, err := s.AllocGPUs(context.Background(), "full", "occ", 2, 2); err != nil {
		t.Fatalf("seed alloc: %v", err)
	}

	n, err := Select(context.Background(), s, ResourceRequest{GPUCount: 1})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if n.ID != "free" {
		t.Errorf("expected to pick 'free', got %s", n.ID)
	}
}

func TestSelect_OfflineNodesIgnored(t *testing.T) {
	s := openStore(t)
	upsertNode(t, s, &store.Node{
		ID: "off", MachineID: "m-off", Name: "off", State: "offline", GpuCount: 4,
	})
	_, err := Select(context.Background(), s, ResourceRequest{GPUCount: 1})
	if !errors.Is(err, ErrNoNode) {
		t.Errorf("expected ErrNoNode, got %v", err)
	}
	if !strings.Contains(err.Error(), "0 online") {
		t.Errorf("expected '0 online' in rejection, got %v", err)
	}
}

func TestSelect_RejectionMessageIncludesCpuAndMem(t *testing.T) {
	s := openStore(t)
	upsertNode(t, s, &store.Node{
		ID: "small", MachineID: "m-small", Name: "small", State: "online",
		CPUCores: 2, MemTotalBytes: 1 << 30, // 1 GiB
	})
	_, err := Select(context.Background(), s, ResourceRequest{
		CPUCores:    8,
		MemoryBytes: 16 << 30, // 16 GiB
	})
	if err == nil {
		t.Fatal("expected rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, ">=8.00 CPU cores") {
		t.Errorf("missing cpu reason: %v", err)
	}
	if !strings.Contains(msg, "bytes memory") {
		t.Errorf("missing memory reason: %v", err)
	}
}

func TestFreeGPUs_HelperBehavior(t *testing.T) {
	cases := []struct {
		total, used, want uint32
	}{
		{4, 0, 4},
		{4, 1, 3},
		{4, 4, 0},
		{4, 5, 0}, // overcommit reported as 0, not negative
		{0, 0, 0},
	}
	for _, c := range cases {
		n := &store.Node{ID: "x", GpuCount: c.total}
		got := FreeGPUs(n, map[string]uint32{"x": c.used})
		if got != c.want {
			t.Errorf("total=%d used=%d -> got %d, want %d", c.total, c.used, got, c.want)
		}
	}
}
