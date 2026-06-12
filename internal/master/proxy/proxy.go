package proxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pumpkinpie/pumpkinpie/internal/master/agentmgr"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

// PortRange is the inclusive range of host ports the master binds for
// container proxies. 30000-32767 fits in the IANA dynamic/private range
// and avoids collision with well-known services.
const (
	PortRangeMin uint32 = 30000
	PortRangeMax uint32 = 32767
)

// Server is a reverse proxy that forwards external HTTP traffic to Docker
// containers running on remote agents, tunneling bytes through gRPC.
//
// Each container gets one dedicated port in the configured range. The
// master opens a fresh net.Listener on demand (when a container is
// created) and closes it when the container is deleted, so the number of
// open file descriptors equals the number of running containers — not the
// size of the port range.
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
	containerID   string
	nodeID        string
	containerPort uint32
	// hostPort is the port Docker bound on the agent host. Sent to the
	// agent in OpenTunnel so the agent can dial `127.0.0.1:hostPort`
	// directly instead of reverse-querying Docker. 0 is allowed and
	// means "fall back to Docker lookup" (legacy masters, or rows
	// persisted before the field existed).
	hostPort uint32
}

func New(mgr *agentmgr.Manager) *Server {
	return &Server{
		mgr:       mgr,
		listeners: make(map[uint32]net.Listener),
		routes:    make(map[uint32]*routeEntry),
	}
}

// AllocatePort reserves a free port in [PortRangeMin, PortRangeMax].
// Returns 0 if no port is free.
func (s *Server) AllocatePort() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := PortRangeMin; p <= PortRangeMax; p++ {
		if _, used := s.routes[p]; used {
			continue
		}
		return p
	}
	return 0
}

// BindPort opens a net.Listener on the given port and starts serving
// HTTP traffic for it. Must be called once per allocated port.
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

	srv := &http.Server{
		Handler:           http.HandlerFunc(s.makeHandler(actual)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("proxy: bound :%d", actual)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("proxy: serve :%d: %v", actual, err)
		}
	}()
	return nil
}

func (s *Server) makeHandler(port uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		rt, ok := s.routes[port]
		s.mu.Unlock()
		if !ok {
			http.Error(w, fmt.Sprintf("no container routed to port %d", port), 404)
			return
		}
		sess := s.mgr.Get(rt.nodeID)
		if sess == nil {
			http.Error(w, "node offline", 502)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking not supported", 500)
			return
		}
		clientConn, _, err := hj.Hijack()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		go s.bridge(clientConn, sess, rt, r)
	}
}

// RegisterRoute stores the route for a port. Caller must have already
// allocated the port and called BindPort. hostPort is the port Docker
// bound on the agent host — passed verbatim in OpenTunnel so the agent
// can skip its own Docker port-mapping lookup. 0 means "agent falls
// back to Docker".
func (s *Server) RegisterRoute(port uint32, containerID, nodeID string, containerPort, hostPort uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[port] = &routeEntry{
		containerID:   containerID,
		nodeID:        nodeID,
		containerPort: containerPort,
		hostPort:      hostPort,
	}
	log.Printf("proxy: route :%d -> container %s on node %s (container %d / agent %d)", port, containerID, nodeID, containerPort, hostPort)
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
// metadata, but does not bind the port (caller should BindPort). hostPort
// is the agent-side port from the create request (now stored in
// ports_json); pass 0 if not known — the agent will fall back to its
// Docker lookup.
func (s *Server) LoadExistingRoute(port uint32, containerID, nodeID string, containerPort, hostPort uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[port] = &routeEntry{
		containerID:   containerID,
		nodeID:        nodeID,
		containerPort: containerPort,
		hostPort:      hostPort,
	}
}

func (s *Server) bridge(clientConn net.Conn, sess *agentmgr.Session, rt *routeEntry, r *http.Request) {
	defer clientConn.Close()
	tunnelID, err := randomID()
	if err != nil {
		respondError(clientConn, "id gen failed")
		return
	}
	sess.RegisterProxy(tunnelID, 64)
	defer sess.UnregisterProxy(tunnelID)

	if err := sess.Send(&pb.MasterMessage{
		Payload: &pb.MasterMessage_OpenTunnel{
			OpenTunnel: &pb.OpenTunnel{
				TunnelId:      tunnelID,
				ContainerPort: rt.containerPort,
				ContainerId:   rt.containerID,
				HostPort:      rt.hostPort,
			},
		},
	}); err != nil {
		respondError(clientConn, "send open tunnel: "+err.Error())
		return
	}

	// WebSocket / h2c / other upgrade requests need the underlying conn
	// to stay open in both directions after the initial handshake. For
	// plain HTTP, the request is fully serialized below and the response
	// flows back through dataCh — no further client→container bytes are
	// expected, so we can safely half-close the write side via CloseAfter.
	isUpgrade := isUpgradeRequest(r)

	// Serialize the parsed request (line + headers + body) to a buffer
	// to send over the gRPC tunnel. r.Write handles Content-Length or
	// chunked encoding for the body.
	var reqBuf []byte
	bw := bufio.NewWriter(byteSliceWriter{&reqBuf})
	if err := r.Write(bw); err != nil {
		respondError(clientConn, "serialize request: "+err.Error())
		return
	}
	if err := bw.Flush(); err != nil {
		respondError(clientConn, "flush: "+err.Error())
		return
	}

	if err := sess.Send(&pb.MasterMessage{
		Payload: &pb.MasterMessage_ProxyData{
			ProxyData: &pb.ProxyTunnelData{
				TunnelId:   tunnelID,
				Data:       reqBuf,
				CloseAfter: !isUpgrade,
			},
		},
	}); err != nil {
		respondError(clientConn, "send request: "+err.Error())
		return
	}

	// For upgrade requests, the client will keep writing data (WS frames,
	// h2c frames) that must reach the container. Start a reader that
	// forwards those bytes as additional ProxyData messages and signals
	// the agent to half-close when the client eventually closes the conn.
	if isUpgrade {
		go s.bridgeClientToAgent(clientConn, sess, tunnelID)
	}

	dataCh := sess.ProxyDataChan(tunnelID)
	closeCh := sess.ProxyCloseChan(tunnelID)
	for {
		select {
		case d, ok := <-dataCh:
			if !ok {
				return
			}
			if _, err := clientConn.Write(d.Data); err != nil {
				return
			}
		case reason := <-closeCh:
			log.Printf("proxy: tunnel %s closed: %s", tunnelID, reason)
			return
		case <-sess.Closed():
			return
		}
	}
}

// bridgeClientToAgent reads bytes the browser writes after the initial
// request (WS frames, h2c frames) and forwards them to the agent as
// ProxyData. When the client closes the conn, it sends ProxyClose so the
// agent can half-close its own write side and the container sees EOF.
// Runs only for upgrade requests — for plain HTTP the request is fully
// serialized in bridge() and no further client→container traffic is
// expected on the same tunnel.
func (s *Server) bridgeClientToAgent(clientConn net.Conn, sess *agentmgr.Session, tunnelID string) {
	buf := make([]byte, 32*1024)
	for {
		n, err := clientConn.Read(buf)
		if n > 0 {
			if sendErr := sess.Send(&pb.MasterMessage{
				Payload: &pb.MasterMessage_ProxyData{
					ProxyData: &pb.ProxyTunnelData{
						TunnelId:   tunnelID,
						Data:       buf[:n],
						CloseAfter: false,
					},
				},
			}); sendErr != nil {
				log.Printf("proxy: tunnel %s client->agent send: %v", tunnelID, sendErr)
				return
			}
		}
		if err != nil {
			// Client closed (or read error). Tell the agent to
			// half-close its write side so the container can finish
			// processing. Best-effort: the tunnel is going away
			// regardless once the conn is closed.
			_ = sess.Send(&pb.MasterMessage{
				Payload: &pb.MasterMessage_ProxyClose{
					ProxyClose: &pb.ProxyTunnelClose{
						TunnelId: tunnelID,
						Reason:   "client closed",
					},
				},
			})
			return
		}
	}
}

// isUpgradeRequest reports whether the request is asking to switch
// protocols mid-connection (WebSocket, h2c, etc.). For these the
// proxy must keep the conn open in both directions instead of
// half-closing after the handshake.
func isUpgradeRequest(r *http.Request) bool {
	for _, conn := range r.Header.Values("Connection") {
		for _, part := range strings.Split(conn, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "Upgrade") {
				return true
			}
		}
	}
	return false
}

func respondError(c net.Conn, msg string) {
	body := msg
	header := "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\nConnection: close\r\n\r\n"
	_, _ = c.Write([]byte(header))
	_, _ = c.Write([]byte(body))
	_ = c.Close()
}

func randomID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type byteSliceWriter struct{ buf *[]byte }

func (w byteSliceWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}
