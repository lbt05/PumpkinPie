package agentmgr

import (
	"context"
	"path/filepath"
	"testing"

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
	return NewManager(st)
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
