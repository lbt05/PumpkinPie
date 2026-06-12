// Package proxy is the master-side reverse proxy that exposes each
// container's published port as a dedicated listener on the master host.
//
// Since Docker on the agent binds the container's host port on 0.0.0.0
// (see internal/agent/docker/client.go), the master can dial the agent
// over plain TCP — no in-band gRPC tunnel is required. For each accepted
// connection on port P we look up the route, dial agentHost:hostPort,
// and io.Copy bytes both ways. This is protocol-agnostic, so HTTP,
// WebSocket, HTTP/2, raw TCP services, etc. all work transparently.
package proxy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/pumpkinpie/pumpkinpie/internal/master/agentmgr"
)

// PortRange is the inclusive range of host ports the master binds for
// container proxies. 30000-32767 fits in the IANA dynamic/private range
// and avoids collision with well-known services.
const (
	PortRangeMin uint32 = 30000
	PortRangeMax uint32 = 32767
)

// dialTimeout caps how long we'll wait when dialing the agent's
// published container port. The agent should be reachable on the
// master's overlay/LAN at gRPC peer-time, so anything longer than a
// few seconds is almost certainly a routing problem rather than a
// busy server.
const dialTimeout = 5 * time.Second

// Server is a TCP reverse proxy that forwards external traffic to a
// container's published port on the owning agent.
//
// Each container gets one dedicated listener on the master (allocated
// in PortRangeMin..PortRangeMax). The agent-side host port is recorded
// on the route at create time and passed verbatim to net.Dial; the
// agent's gRPC peer address (captured at Connect) supplies the host.
//
// The number of open file descriptors on the master scales with the
// number of active TCP connections plus one listener per running
// container — not with the size of the port range.
type Server struct {
	mgr *agentmgr.Manager

	mu sync.Mutex

	// port -> active listener; nil if port is allocated but not yet bound
	// (shouldn't normally happen, but keeps the map authoritative)
	listeners map[uint32]net.Listener

	// port -> route (container that owns the port)
	routes map[uint32]*routeEntry
}

type routeEntry struct {
	containerID string
	nodeID      string
	// hostPort is the port Docker bound on the agent host's 0.0.0.0,
	// i.e. the "X" in `docker run -p X:Y`. The master dials
	// `<agent gRPC peer host>:hostPort` directly.
	hostPort uint32
}

func New(mgr *agentmgr.Manager) *Server {
	return &Server{
		mgr:       mgr,
		listeners: make(map[uint32]net.Listener),
		routes:    make(map[uint32]*routeEntry),
	}
}

// BindPort opens a net.Listener on the given port and starts an
// accept loop that forwards every accepted connection to the route's
// upstream. Must be called once per allocated port.
func (s *Server) BindPort(ctx context.Context, port uint32) error {
	s.mu.Lock()
	if _, exists := s.listeners[port]; exists {
		s.mu.Unlock()
		return fmt.Errorf("port %d already bound", port)
	}
	s.mu.Unlock()

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen :%d: %w", port, err)
	}
	actual := uint32(ln.Addr().(*net.TCPAddr).Port)

	s.mu.Lock()
	s.listeners[actual] = ln
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	go s.acceptLoop(ln, actual)
	log.Printf("proxy: bound :%d", actual)
	return nil
}

func (s *Server) acceptLoop(ln net.Listener, port uint32) {
	for {
		client, err := ln.Accept()
		if err != nil {
			// Listener was closed (UnregisterRoute or ctx cancellation);
			// quietly exit. Any other error here is fatal for this port.
			return
		}
		go s.forward(client, port)
	}
}

// forward is the per-connection bridge: look up the route, dial the
// agent host's published port, and io.Copy bytes both ways until either
// side terminates.
func (s *Server) forward(client net.Conn, port uint32) {
	defer client.Close()

	s.mu.Lock()
	rt, ok := s.routes[port]
	s.mu.Unlock()
	if !ok {
		log.Printf("proxy: :%d no route registered", port)
		return
	}
	sess := s.mgr.Get(rt.nodeID)
	if sess == nil {
		log.Printf("proxy: :%d node %s offline", port, rt.nodeID)
		return
	}
	host := sess.RemoteHost()
	if host == "" {
		log.Printf("proxy: :%d no remote host known for node %s", port, rt.nodeID)
		return
	}
	addr := net.JoinHostPort(host, strconv.Itoa(int(rt.hostPort)))
	upstream, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		log.Printf("proxy: :%d dial %s: %v", port, addr, err)
		return
	}
	defer upstream.Close()

	log.Printf("proxy: :%d -> %s (container %s)", port, addr, rt.containerID)
	pipe(client, upstream)
}

// pipe shuttles bytes between a and b until both directions complete.
// Either side hitting EOF / error half-closes the write side of the
// other so it sees a clean EOF, then we wait for the second goroutine
// before returning (the deferred Close calls on a and b in the caller
// then tear everything down). Half-close requires *net.TCPConn; for
// any other concrete type we fall back to a hard close on first error.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(b, a)
		closeWrite(b)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		closeWrite(a)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// RegisterRoute stores the route for a port. Caller must have already
// called BindPort. hostPort is the agent-side published port (the "X"
// in `docker run -p X:Y`) that the master will dial.
func (s *Server) RegisterRoute(port uint32, containerID, nodeID string, hostPort uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[port] = &routeEntry{
		containerID: containerID,
		nodeID:      nodeID,
		hostPort:    hostPort,
	}
	log.Printf("proxy: route :%d -> container %s on node %s (agent port %d)", port, containerID, nodeID, hostPort)
}

// UnregisterRoute removes the route and closes the listener for the port.
// Returns the port that was freed (or 0 if not registered).
func (s *Server) UnregisterRoute(port uint32) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.routes, port)
	if ln, ok := s.listeners[port]; ok {
		_ = ln.Close()
		delete(s.listeners, port)
		log.Printf("proxy: released :%d", port)
	}
	return port
}

// LoadExistingRoute registers a route at master startup from persisted
// metadata, but does not bind the port (caller should BindPort).
func (s *Server) LoadExistingRoute(port uint32, containerID, nodeID string, hostPort uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[port] = &routeEntry{
		containerID: containerID,
		nodeID:      nodeID,
		hostPort:    hostPort,
	}
}
