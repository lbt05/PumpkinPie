package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedNode + seedContainer satisfy the FKs gpu_alloc relies on.
func seedNode(t *testing.T, s *Store, id string, gpus uint32) {
	t.Helper()
	if err := s.UpsertNode(context.Background(), &Node{
		ID:            id,
		MachineID:     "machine-" + id,
		Name:          id,
		State:         "online",
		LastHeartbeat: time.Now(),
		RegisteredAt:  time.Now(),
		GpuCount:      gpus,
	}); err != nil {
		t.Fatalf("seed node %s: %v", id, err)
	}
}

func seedContainer(t *testing.T, s *Store, id, nodeID string) {
	t.Helper()
	if err := s.InsertContainer(context.Background(), &Container{
		ID:     id,
		NodeID: nodeID,
		Image:  "test:latest",
		State:  "created",
		Status: "created",
	}); err != nil {
		t.Fatalf("seed container %s: %v", id, err)
	}
}

func TestAllocGPUs_AssignsLowestFirst(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "n1", 4)
	seedContainer(t, s, "c1", "n1")

	idx, err := s.AllocGPUs(ctx, "n1", "c1", 2, 4)
	if err != nil {
		t.Fatalf("AllocGPUs: %v", err)
	}
	if len(idx) != 2 || idx[0] != 0 || idx[1] != 1 {
		t.Errorf("expected [0 1], got %v", idx)
	}

	seedContainer(t, s, "c2", "n1")
	idx2, err := s.AllocGPUs(ctx, "n1", "c2", 2, 4)
	if err != nil {
		t.Fatalf("second AllocGPUs: %v", err)
	}
	if len(idx2) != 2 || idx2[0] != 2 || idx2[1] != 3 {
		t.Errorf("expected [2 3], got %v", idx2)
	}
}

func TestAllocGPUs_RejectsWhenInsufficient(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "n1", 2)
	seedContainer(t, s, "c1", "n1")

	if _, err := s.AllocGPUs(ctx, "n1", "c1", 2, 2); err != nil {
		t.Fatalf("first alloc should succeed: %v", err)
	}
	seedContainer(t, s, "c2", "n1")
	_, err := s.AllocGPUs(ctx, "n1", "c2", 1, 2)
	if !errors.Is(err, ErrInsufficientGPUs) {
		t.Errorf("expected ErrInsufficientGPUs, got %v", err)
	}
	// Nothing must have been written for c2.
	got, _ := s.GetContainerGPUs(ctx, "c2")
	if len(got) != 0 {
		t.Errorf("expected no rows for c2, got %v", got)
	}
}

func TestAllocGPUs_ConcurrentSafe(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "n1", 4)

	const N = 8 // twice as many requesters as GPUs
	for i := 0; i < N; i++ {
		seedContainer(t, s, fmt.Sprintf("c%d", i), "n1")
	}

	var wg sync.WaitGroup
	wg.Add(N)
	results := make([][]uint32, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			r, err := s.AllocGPUs(ctx, "n1", fmt.Sprintf("c%d", i), 1, 4)
			results[i] = r
			errs[i] = err
		}()
	}
	wg.Wait()

	got := map[uint32]int{}
	successes := 0
	for i, r := range results {
		if errs[i] == nil {
			successes++
			if len(r) != 1 {
				t.Errorf("goroutine %d: expected 1 index, got %v", i, r)
				continue
			}
			if prev, dup := got[r[0]]; dup {
				t.Errorf("DUPLICATE index %d: goroutine %d and %d", r[0], prev, i)
			}
			got[r[0]] = i
		} else if !errors.Is(errs[i], ErrInsufficientGPUs) && !errors.Is(errs[i], ErrGPUTaken) {
			t.Errorf("goroutine %d: unexpected err %v", i, errs[i])
		}
	}
	if successes != 4 {
		t.Errorf("expected exactly 4 successes on a 4-GPU node, got %d", successes)
	}
}

func TestAllocSpecificGPUs_AllOrNothing(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "n1", 4)
	seedContainer(t, s, "c1", "n1")
	seedContainer(t, s, "c2", "n1")

	if _, err := s.AllocGPUs(ctx, "n1", "c1", 1, 4); err != nil {
		t.Fatal(err)
	}
	c1Idx, _ := s.GetContainerGPUs(ctx, "c1")
	if len(c1Idx) != 1 || c1Idx[0] != 0 {
		t.Fatalf("expected c1=[0], got %v", c1Idx)
	}

	// Try to reserve [1,0,2] for c2 — index 0 is held by c1.
	err := s.AllocSpecificGPUs(ctx, "n1", "c2", []uint32{1, 0, 2})
	if !errors.Is(err, ErrGPUTaken) {
		t.Fatalf("expected ErrGPUTaken, got %v", err)
	}
	c2Idx, _ := s.GetContainerGPUs(ctx, "c2")
	if len(c2Idx) != 0 {
		t.Errorf("partial reservation leaked: c2 holds %v", c2Idx)
	}

	// Pure conflict-free reservation works.
	if err := s.AllocSpecificGPUs(ctx, "n1", "c2", []uint32{1, 3}); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	c2Idx, _ = s.GetContainerGPUs(ctx, "c2")
	if len(c2Idx) != 2 || c2Idx[0] != 1 || c2Idx[1] != 3 {
		t.Errorf("expected c2=[1 3], got %v", c2Idx)
	}
}

func TestFreeGPUs_ReleasesAll(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "n1", 4)
	seedContainer(t, s, "c1", "n1")

	if _, err := s.AllocGPUs(ctx, "n1", "c1", 3, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.FreeGPUs(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetContainerGPUs(ctx, "c1")
	if len(got) != 0 {
		t.Errorf("expected 0 rows after FreeGPUs, got %v", got)
	}
	usage, _ := s.GPUUsageByNode(ctx)
	if usage["n1"] != 0 {
		t.Errorf("expected 0 usage after free, got %d", usage["n1"])
	}
}

func TestFreeGPUs_CascadeOnContainerDelete(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "n1", 4)
	seedContainer(t, s, "c1", "n1")

	if _, err := s.AllocGPUs(ctx, "n1", "c1", 2, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteContainer(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetContainerGPUs(ctx, "c1")
	if len(got) != 0 {
		t.Errorf("expected FK CASCADE to drop gpu_alloc rows, still have %v", got)
	}
}

func TestGPUUsageByNode_ReportsCounts(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "nA", 8)
	seedNode(t, s, "nB", 4)
	seedContainer(t, s, "cA1", "nA")
	seedContainer(t, s, "cB1", "nB")

	if _, err := s.AllocGPUs(ctx, "nA", "cA1", 3, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllocGPUs(ctx, "nB", "cB1", 1, 4); err != nil {
		t.Fatal(err)
	}
	usage, err := s.GPUUsageByNode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage["nA"] != 3 {
		t.Errorf("nA usage = %d, want 3", usage["nA"])
	}
	if usage["nB"] != 1 {
		t.Errorf("nB usage = %d, want 1", usage["nB"])
	}
}

func TestAllocGPUs_ZeroCountNoop(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "n1", 4)
	seedContainer(t, s, "c1", "n1")

	idx, err := s.AllocGPUs(ctx, "n1", "c1", 0, 4)
	if err != nil {
		t.Fatalf("zero-count alloc should be a no-op, got %v", err)
	}
	if idx != nil {
		t.Errorf("expected nil indices, got %v", idx)
	}
}

func TestListGPUAllocsForNode_JoinsContainerNames(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "n1", 4)
	// Insert containers with explicit names so we know what to assert.
	if err := s.InsertContainer(ctx, &Container{
		ID: "c1", NodeID: "n1", Name: "training-job", Image: "x", State: "running", Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertContainer(ctx, &Container{
		ID: "c2", NodeID: "n1", Name: "serve", Image: "x", State: "running", Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllocGPUs(ctx, "n1", "c1", 2, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.AllocSpecificGPUs(ctx, "n1", "c2", []uint32{3}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListGPUAllocsForNode(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 allocs, got %d", len(got))
	}
	// Ascending order by gpu_index.
	want := []GPUAlloc{
		{Index: 0, ContainerID: "c1", ContainerName: "training-job"},
		{Index: 1, ContainerID: "c1", ContainerName: "training-job"},
		{Index: 3, ContainerID: "c2", ContainerName: "serve"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("alloc[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestListGPUAllocsForNode_ScopedToNode(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "nA", 4)
	seedNode(t, s, "nB", 4)
	seedContainer(t, s, "cA", "nA")
	seedContainer(t, s, "cB", "nB")
	if _, err := s.AllocGPUs(ctx, "nA", "cA", 2, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllocGPUs(ctx, "nB", "cB", 1, 4); err != nil {
		t.Fatal(err)
	}

	a, err := s.ListGPUAllocsForNode(ctx, "nA")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 2 {
		t.Errorf("nA: expected 2 rows, got %d", len(a))
	}
	for _, x := range a {
		if x.ContainerID != "cA" {
			t.Errorf("nA leaked row from %s", x.ContainerID)
		}
	}

	b, err := s.ListGPUAllocsForNode(ctx, "nB")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || b[0].ContainerID != "cB" {
		t.Errorf("nB = %+v", b)
	}
}

func TestListGPUAllocsForNode_EmptyWhenNoneHeld(t *testing.T) {
	s, _ := openTestStore(t)
	seedNode(t, s, "n1", 4)
	got, err := s.ListGPUAllocsForNode(context.Background(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}

// TestAllocGPUs_RequiresContainerFirst pins the FK contract that the
// api.createContainer handler depends on: gpu_alloc.container_id
// REFERENCES containers(id), so the container row MUST exist before
// any GPU reservation is made. The api previously called AllocGPUs
// before InsertContainer and got back "FOREIGN KEY constraint failed
// (787)" from SQLite (foreign_keys=ON). If you re-order those calls
// in the api, this test will still pass — but the integration symptom
// will reappear in the field. Treat this test as the contract: the
// store is correct, the ordering is the caller's responsibility.
func TestAllocGPUs_RequiresContainerFirst(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedNode(t, s, "n1", 4)

	// No container row exists yet — must fail.
	if _, err := s.AllocGPUs(ctx, "n1", "ghost", 1, 4); err == nil {
		t.Fatal("expected FK error for unknown container_id, got nil")
	} else if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("expected FOREIGN KEY error, got: %v", err)
	}

	// Insert the container, then alloc — must succeed.
	seedContainer(t, s, "c1", "n1")
	if _, err := s.AllocGPUs(ctx, "n1", "c1", 1, 4); err != nil {
		t.Fatalf("alloc after insert should succeed, got %v", err)
	}

	// Same contract for AllocSpecificGPUs.
	if err := s.AllocSpecificGPUs(ctx, "n1", "ghost2", []uint32{2}); err == nil {
		t.Fatal("expected FK error from AllocSpecificGPUs, got nil")
	} else if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("expected FOREIGN KEY error, got: %v", err)
	}
}
