package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// openTestStore opens a fresh Store backed by a unique temp file. Closing
// the store and removing the file is the caller's responsibility (or use
// t.TempDir + t.Cleanup).
func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = os.Remove(dbPath)
	})
	return s, dbPath
}

// range30000 = the production port range the master uses.
const (
	testPortStart uint32 = 30000
	testPortEnd   uint32 = 30010 // small range so we can exhaust it
	testContainer        = "c-test"
)

// 1. Single-threaded sequential allocation never returns the same port.
func TestAllocPort_NoDuplicates_Sequential(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	seen := make(map[uint32]bool)
	for i := 0; i < 5; i++ {
		p, err := s.AllocPort(ctx, testPortStart, testPortEnd, testContainer)
		if err != nil {
			t.Fatalf("alloc #%d: %v", i, err)
		}
		if p < testPortStart || p > testPortEnd {
			t.Errorf("alloc #%d returned %d outside [%d,%d]", i, p, testPortStart, testPortEnd)
		}
		if seen[p] {
			t.Errorf("duplicate port %d on iteration %d", p, i)
		}
		seen[p] = true
	}
	if len(seen) != 5 {
		t.Errorf("expected 5 distinct ports, got %d", len(seen))
	}
}

// 2. 100 goroutines racing to allocate must each get a unique port.
//    This is the regression test for "no duplicate port will be assigned
//    across the cluster" — the in-process invariant that protects us
//    even before the SQLite UNIQUE constraint is consulted.
func TestAllocPort_NoDuplicates_Concurrent(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	const N = 100
	results := make([]uint32, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			// Use a wider range than N so we don't exhaust it.
			p, err := s.AllocPort(ctx, 30000, 30100, fmt.Sprintf("c-%d", i))
			results[i] = p
			errs[i] = err
		}()
	}
	wg.Wait()

	seen := make(map[uint32]int)
	for i, p := range results {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if p < 30000 || p > 30100 {
			t.Errorf("goroutine %d returned %d outside range", i, p)
		}
		if prev, dup := seen[p]; dup {
			t.Errorf("DUPLICATE port %d: first assigned to goroutine %d, then to %d", p, prev, i)
		}
		seen[p] = i
	}
	if len(seen) != N {
		t.Errorf("expected %d distinct ports, got %d", N, len(seen))
	}
}

// 3. After master restart (close + reopen same file), the port_alloc
//    table is loaded back and the same ports must NOT be re-assigned.
//    This is the "across the cluster" guarantee after a master crash.
func TestAllocPort_PersistsAcrossRestart(t *testing.T) {
	_, dbPath := openTestStore(t)

	// First "session": allocate the first 3 ports.
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	ctx := context.Background()
	first := make(map[uint32]bool)
	for i := 0; i < 3; i++ {
		p, err := s1.AllocPort(ctx, testPortStart, testPortEnd, testContainer)
		if err != nil {
			t.Fatalf("session1 alloc #%d: %v", i, err)
		}
		first[p] = true
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close session1: %v", err)
	}

	// Second "session": reopen, allocate, must skip the 3 already taken.
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	for i := 0; i < 3; i++ {
		p, err := s2.AllocPort(ctx, testPortStart, testPortEnd, testContainer)
		if err != nil {
			t.Fatalf("session2 alloc #%d: %v", i, err)
		}
		if first[p] {
			t.Errorf("port %d re-allocated after restart (was in session1)", p)
		}
	}
}

// 4. FreePort returns a port to the pool; the next allocation may
//    reuse it (current implementation: AllocPort always picks the
//    smallest free port, so the just-freed one comes back first).
func TestAllocPort_FreeThenReuse(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	p1, err := s.AllocPort(ctx, testPortStart, testPortEnd, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.AllocPort(ctx, testPortStart, testPortEnd, "c-2")
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatalf("sequential alloc returned same port %d", p1)
	}

	if err := s.FreePort(ctx, p1); err != nil {
		t.Fatalf("free: %v", err)
	}

	p3, err := s.AllocPort(ctx, testPortStart, testPortEnd, "c-3")
	if err != nil {
		t.Fatal(err)
	}
	if p3 != p1 {
		t.Errorf("expected freed port %d to be re-allocated, got %d", p1, p3)
	}
	if p3 == p2 {
		t.Errorf("re-allocated port %d collides with still-allocated %d", p3, p2)
	}
}

// 5. Exhausting the range must return an error (not a silent wrap or
//    panic). Caller can then decide to evict / refuse / extend.
func TestAllocPort_Exhaustion(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	// Fill the entire range.
	for i := uint32(0); i < (testPortEnd - testPortStart + 1); i++ {
		if _, err := s.AllocPort(ctx, testPortStart, testPortEnd, testContainer); err != nil {
			t.Fatalf("alloc #%d failed prematurely: %v", i, err)
		}
	}
	// One more must fail.
	p, err := s.AllocPort(ctx, testPortStart, testPortEnd, testContainer)
	if err == nil {
		t.Errorf("expected exhaustion error, got port %d", p)
	}
	if p != 0 {
		t.Errorf("expected p=0 on exhaustion, got %d", p)
	}
}

// 6. Many alloc/free cycles must never produce a duplicate within the
//    "live set" (i.e. ports currently allocated). Even if freed ports
//    can be reused, the live set must be unique.
func TestAllocPort_NoDuplicateAfterChurn(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	live := make(map[uint32]bool)
	// Use a wider range so we have room to churn.
	const start, end uint32 = 30000, 30099
	for round := 0; round < 200; round++ {
		// Allocate 5.
		justAlloc := make([]uint32, 0, 5)
		for i := 0; i < 5; i++ {
			p, err := s.AllocPort(ctx, start, end, testContainer)
			if err != nil {
				t.Fatalf("round %d alloc: %v", round, err)
			}
			if live[p] {
				t.Fatalf("round %d: duplicate live port %d", round, p)
			}
			live[p] = true
			justAlloc = append(justAlloc, p)
		}
		// Free them all.
		for _, p := range justAlloc {
			if err := s.FreePort(ctx, p); err != nil {
				t.Fatalf("round %d free: %v", round, err)
			}
			delete(live, p)
		}
	}
	if len(live) != 0 {
		t.Errorf("after churn, %d ports still marked live", len(live))
	}
}
