package proxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// TestRouteLifecycle verifies the bookkeeping for routes / listeners
// independently of the dialing path (which needs a real agent).
func TestRouteLifecycle(t *testing.T) {
	s := New(nil) // mgr only consulted on forward(); these calls don't touch it.

	s.RegisterRoute(30000, "container-A", "node-1", 9000)
	if got := s.routes[30000]; got == nil || got.hostPort != 9000 || got.nodeID != "node-1" {
		t.Fatalf("RegisterRoute did not record route correctly: %+v", got)
	}

	s.LoadExistingRoute(30001, "container-B", "node-2", 9001)
	if got := s.routes[30001]; got == nil || got.containerID != "container-B" {
		t.Fatalf("LoadExistingRoute did not record route: %+v", got)
	}

	freed := s.UnregisterRoute(30000)
	if freed != 30000 {
		t.Fatalf("UnregisterRoute returned %d, want 30000", freed)
	}
	if _, ok := s.routes[30000]; ok {
		t.Fatal("route 30000 still present after UnregisterRoute")
	}
}

// TestPipeBidirectional exercises the byte-shuttle that backs forward().
// Two pairs of net.Pipe stand in for the client<->master and
// master<->upstream halves; we then write on each end and verify the
// bytes appear on the other.
func TestPipeBidirectional(t *testing.T) {
	clientLocal, clientRemote := net.Pipe()
	upstreamLocal, upstreamRemote := net.Pipe()
	defer clientLocal.Close()
	defer upstreamRemote.Close()

	done := make(chan struct{})
	go func() {
		pipe(clientRemote, upstreamLocal)
		close(done)
	}()

	// client -> upstream
	go func() { _, _ = clientLocal.Write([]byte("ping")) }()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(upstreamRemote, buf); err != nil {
		t.Fatalf("read from upstream: %v", err)
	}
	if !bytes.Equal(buf, []byte("ping")) {
		t.Fatalf("upstream got %q, want %q", buf, "ping")
	}

	// upstream -> client
	go func() { _, _ = upstreamRemote.Write([]byte("pong")) }()
	if _, err := io.ReadFull(clientLocal, buf); err != nil {
		t.Fatalf("read from client: %v", err)
	}
	if !bytes.Equal(buf, []byte("pong")) {
		t.Fatalf("client got %q, want %q", buf, "pong")
	}

	// Close one end; pipe() must tear down both directions and return.
	_ = clientLocal.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipe() did not return after client closed")
	}
}

// TestBindPortAcceptsAndTearsDown checks that BindPort actually opens
// a listener and that UnregisterRoute closes it. A connection that
// hits an unrouted port should be closed by forward() immediately,
// which the dialer observes as an EOF on Read.
func TestBindPortAcceptsAndTearsDown(t *testing.T) {
	s := New(nil)

	// Grab a free port without holding it open.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := uint32(probe.Addr().(*net.TCPAddr).Port)
	_ = probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.BindPort(ctx, port); err != nil {
		t.Fatalf("BindPort: %v", err)
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), time.Second)
	if err != nil {
		t.Fatalf("dial bound port: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Error("expected EOF on unrouted port, got data")
	}
	_ = conn.Close()

	// UnregisterRoute closes the listener even with no route registered.
	s.UnregisterRoute(port)

	// New dial after teardown should fail (listener is gone).
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 200*time.Millisecond); err == nil {
		_ = c.Close()
		t.Error("expected dial to fail after UnregisterRoute, succeeded")
	}
}
