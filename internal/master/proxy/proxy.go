package proxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pumpkinpie/pumpkinpie/internal/master/agentmgr"
	"github.com/pumpkinpie/pumpkinpie/internal/master/store"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

// Server is a reverse proxy that forwards external HTTP traffic to Docker
// containers running on remote agents, tunneling bytes through gRPC.
//
// URL scheme:  http://<master>:<proxy-port>/c/<container_id>/<path>
//
// The proxy server listens on a single port and routes based on the
// container ID embedded in the URL path. This avoids needing to bind
// hundreds of ports when many containers are deployed.
type Server struct {
	store *store.Store
	mgr   *agentmgr.Manager

	mu       sync.Mutex
	listener net.Listener
	port     uint32

	mu2    sync.RWMutex
	routes map[string]*routeEntry // container_id -> route
	// back-compat: external_port -> container_id (for the UI's external_url)
	extPort map[uint32]string
}

type routeEntry struct {
	containerID   string
	nodeID        string
	containerPort uint32
}

func New(s *store.Store, mgr *agentmgr.Manager) *Server {
	return &Server{
		store:    s,
		mgr:      mgr,
		routes:   make(map[string]*routeEntry),
		extPort:  make(map[uint32]string),
	}
}

// Start binds to a port and serves HTTP traffic routed to containers.
func (s *Server) Start(ctx context.Context, port uint32) (uint32, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return 0, err
	}
	actual := uint32(ln.Addr().(*net.TCPAddr).Port)
	s.mu.Lock()
	s.listener = ln
	s.port = actual
	s.mu.Unlock()
	s.loadExistingRoutes(ctx)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		srv := &http.Server{
			Handler:           http.HandlerFunc(s.handle),
			ReadHeaderTimeout: 10 * time.Second,
		}
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("proxy serve: %v", err)
		}
	}()
	log.Printf("proxy listening on :%d (URL scheme: /c/<container_id>/...)", actual)
	return actual, nil
}

func (s *Server) Port() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

func (s *Server) loadExistingRoutes(ctx context.Context) {
	cs, err := s.store.ListContainers(ctx)
	if err != nil {
		return
	}
	for _, c := range cs {
		if c.PortsJSON == "" {
			continue
		}
		ports, _ := parsePorts(c.PortsJSON)
		if len(ports) == 0 {
			continue
		}
		s.RegisterRoute(c.ID, c.NodeID, ports[0].ContainerPort, c.ExternalPort)
	}
}

// RegisterRoute stores a route. externalPort may be 0 (no public URL hint).
func (s *Server) RegisterRoute(containerID, nodeID string, containerPort, externalPort uint32) {
	s.mu2.Lock()
	defer s.mu2.Unlock()
	s.routes[containerID] = &routeEntry{
		containerID:   containerID,
		nodeID:        nodeID,
		containerPort: containerPort,
	}
	if externalPort != 0 {
		s.extPort[externalPort] = containerID
	}
	log.Printf("proxy: container %s on node %s (port %d) -> externalPort=%d", containerID, nodeID, containerPort, externalPort)
}

func (s *Server) UnregisterRoute(containerID string) {
	s.mu2.Lock()
	defer s.mu2.Unlock()
	delete(s.routes, containerID)
	for p, c := range s.extPort {
		if c == containerID {
			delete(s.extPort, p)
		}
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	// Path: /c/<container_id>[/<rest>]
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 || parts[0] != "c" {
		http.Error(w, "expected /c/<container_id>/...", 404)
		return
	}
	cid := parts[1]
	s.mu2.RLock()
	rt, ok := s.routes[cid]
	s.mu2.RUnlock()
	if !ok {
		http.Error(w, fmt.Sprintf("no route for container %s", cid), 404)
		return
	}
	sess := s.mgr.Get(rt.nodeID)
	if sess == nil {
		http.Error(w, "node offline", 502)
		return
	}
	// Rewrite the path to strip /c/<id> prefix so the container sees the
	// original path it was started with.
	r.URL.Path = "/" + parts[2]
	r.RequestURI = ""

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
			},
		},
	}); err != nil {
		respondError(clientConn, "send open tunnel: "+err.Error())
		return
	}

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
				CloseAfter: true,
			},
		},
	}); err != nil {
		respondError(clientConn, "send request: "+err.Error())
		return
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

type portMappingJSON struct {
	ContainerPort uint32 `json:"container_port"`
	Protocol      string `json:"protocol"`
}

func parsePorts(s string) ([]portMappingJSON, error) {
	var out []portMappingJSON
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}
