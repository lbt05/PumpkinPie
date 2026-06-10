package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/pumpkinpie/pumpkinpie/internal/master/agentmgr"
	"github.com/pumpkinpie/pumpkinpie/internal/master/proxy"
	"github.com/pumpkinpie/pumpkinpie/internal/master/scheduler"
	"github.com/pumpkinpie/pumpkinpie/internal/master/store"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
)

type Server struct {
	store    *store.Store
	mgr      *agentmgr.Manager
	proxy    *proxy.Server
	upgrader websocket.Upgrader
	lifetime context.Context
}

func New(ctx context.Context, s *store.Store, m *agentmgr.Manager, p *proxy.Server) *Server {
	return &Server{
		store:    s,
		mgr:      m,
		proxy:    p,
		lifetime: ctx,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (s *Server) proxyPort() uint32 { return 0 } // kept for legacy calls; container URL is now per-port

func (s *Server) Engine() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	c := cors.DefaultConfig()
	c.AllowAllOrigins = true
	c.AllowHeaders = append(c.AllowHeaders, "Authorization")
	r.Use(cors.New(c))

	r.GET("/api/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	r.GET("/api/nodes", s.listNodes)
	r.GET("/api/nodes/:id", s.getNode)
	r.GET("/api/nodes/:id/containers", s.listNodeContainers)

	r.GET("/api/containers", s.listContainers)
	r.GET("/api/containers/:id", s.getContainer)

	r.POST("/api/containers", s.createContainer)
	r.DELETE("/api/containers/:id", s.deleteContainer)
	r.POST("/api/containers/:id/start", s.startContainer)
	r.POST("/api/containers/:id/stop", s.stopContainer)

	r.GET("/api/ws", s.websocket)

	// serve built frontend under /console (Vite base)
	r.StaticFS("/console", http.Dir("./web/dist"))
	r.GET("/console", func(c *gin.Context) { c.Redirect(302, "/console/") })
	r.NoRoute(func(c *gin.Context) {
		// SPA fallback: only for non-API, non-asset paths
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") {
			c.JSON(404, gin.H{"error": "not found"})
			return
		}
		c.File("./web/dist/index.html")
	})
	return r
}

// ---- Nodes ----

func (s *Server) listNodes(c *gin.Context) {
	nodes, err := s.store.ListNodes(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	out := make([]gin.H, 0, len(nodes))
	online := s.mgr.Online()
	onlineSet := map[string]bool{}
	for _, id := range online {
		onlineSet[id] = true
	}
	for _, n := range nodes {
		// use live state from manager when available
		if onlineSet[n.ID] {
			n.State = "online"
		}
		row := gin.H{
			"id":                  n.ID,
			"name":                n.Name,
			"hostname":            n.Hostname,
			"os":                  n.OS,
			"arch":                n.Arch,
			"agent_version":       n.AgentVersion,
			"state":               n.State,
			"last_heartbeat":      n.LastHeartbeat,
			"registered_at":       n.RegisteredAt,
			"cpu_percent":         n.CPUPercent,
			"cpu_cores":           n.CPUCores,
			"mem_used_bytes":      n.MemUsedBytes,
			"mem_total_bytes":     n.MemTotalBytes,
			"load1":               n.Load1,
			"gpu_count":           n.GpuCount,
			"gpu_mem_used_bytes":  n.GpuMemUsed,
			"gpu_mem_total_bytes": n.GpuMemTotal,
			"gpu_usage_percent":   n.GpuUsageAvg,
			"metrics_at":          n.MetricsAt,
		}
		if n.DiskJSON != "" {
			var disks []map[string]any
			_ = json.Unmarshal([]byte(n.DiskJSON), &disks)
			row["disks"] = disks
		}
		if n.GPUJSON != "" {
			var gpus []map[string]any
			_ = json.Unmarshal([]byte(n.GPUJSON), &gpus)
			row["gpus"] = gpus
		}
		out = append(out, row)
	}
	c.JSON(200, gin.H{"nodes": out})
}

func (s *Server) getNode(c *gin.Context) {
	n, err := s.store.GetNode(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, n)
}

func (s *Server) listNodeContainers(c *gin.Context) {
	all, err := s.store.ListContainers(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	out := make([]*store.Container, 0)
	for _, cc := range all {
		if cc.NodeID == c.Param("id") {
			out = append(out, cc)
		}
	}
	c.JSON(200, gin.H{"containers": out})
}

// ---- Containers ----

type createContainerReq struct {
	Image        string   `json:"image"`
	Name         string   `json:"name"`
	Env          []string `json:"env"`
	Cmd          []string `json:"cmd"`
	VolumeBinds  []string `json:"volume_binds"`
	PortMappings []struct {
		ContainerPort uint32 `json:"container_port"`
		Protocol      string `json:"protocol"`
	} `json:"port_mappings"`
	CPUCores    float64 `json:"cpu_cores"`
	MemoryBytes uint64  `json:"memory_bytes"`
	GPUCount    uint32  `json:"gpu_count"`
	NodeID      string  `json:"node_id"` // optional: pin to specific node
	Pull        bool    `json:"pull"`
	ExternalPort uint32 `json:"external_port"` // 0 = auto-allocate
}

func (s *Server) createContainer(c *gin.Context) {
	var req createContainerReq
	if err := c.BindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.Image == "" {
		c.JSON(400, gin.H{"error": "image is required"})
		return
	}

	// Pick a node
	var target *store.Node
	if req.NodeID != "" {
		n, err := s.store.GetNode(c.Request.Context(), req.NodeID)
		if err != nil {
			c.JSON(400, gin.H{"error": "node_id not found"})
			return
		}
		target = n
	} else {
		n, err := scheduler.Select(c.Request.Context(), s.store, scheduler.ResourceRequest{
			CPUCores:    req.CPUCores,
			MemoryBytes: req.MemoryBytes,
			GPUCount:    req.GPUCount,
		})
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error(), "hint": "no suitable node; relax resource requirements or wait for nodes to come online"})
			return
		}
		target = n
	}

	sess := s.mgr.Get(target.ID)
	if sess == nil {
		c.JSON(400, gin.H{"error": "target node not online"})
		return
	}

	containerID := newContainerID()

	// If the user left the name blank, generate a friendly one based on
	// the image, e.g. "pp-nginx-alpine-x7t2c". This still goes to the
	// agent as Docker's container name (must match DNS-1123).
	containerName := req.Name
	if strings.TrimSpace(containerName) == "" {
		containerName = autoName(req.Image)
	}
	// Sanity: agent will reject names that aren't valid. Keep it ASCII-safe
	// and short.
	containerName = sanitizeContainerName(containerName)

	ports := make([]*pb.PortMapping, 0, len(req.PortMappings))
	for _, p := range req.PortMappings {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		ports = append(ports, &pb.PortMapping{
			ContainerPort: p.ContainerPort,
			Protocol:      proto,
		})
	}

	// Allocate external port (use the first container port's mapping)
	externalPort := req.ExternalPort
	if externalPort == 0 && len(ports) > 0 {
		p, err := s.store.AllocPort(c.Request.Context(), 30000, 32767, containerID)
		if err != nil {
			c.JSON(500, gin.H{"error": "alloc port: " + err.Error()})
			return
		}
		externalPort = p
	}

	envJSON, _ := json.Marshal(req.Env)
	cmdJSON, _ := json.Marshal(req.Cmd)
	portsJSON, _ := json.Marshal(ports)
	volBinds, _ := json.Marshal(req.VolumeBinds)

	cc := &store.Container{
		ID:           containerID,
		NodeID:       target.ID,
		Name:         containerName,
		Image:        req.Image,
		State:        "creating",
		Status:       "creating",
		EnvJSON:      string(envJSON),
		CmdJSON:      string(cmdJSON),
		PortsJSON:    string(portsJSON),
		VolumeBinds:  string(volBinds),
		CPUCores:     req.CPUCores,
		MemoryBytes:  req.MemoryBytes,
		GPUCount:     req.GPUCount,
		ExternalPort: externalPort,
	}
	if err := s.store.InsertContainer(c.Request.Context(), cc); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// Register proxy route and bind the listener for this container's port.
	if externalPort != 0 && len(ports) > 0 {
		s.proxy.RegisterRoute(externalPort, containerID, target.ID, ports[0].ContainerPort)
		if err := s.proxy.BindPort(s.lifetime, externalPort); err != nil {
			log.Printf("bind proxy port %d failed: %v (container %s)", externalPort, err, containerID)
		}
	}

	// Send create command to agent
	cmd := &pb.CreateContainerCommand{
		ContainerId: containerID,
		Image:       req.Image,
		Env:         req.Env,
		Cmd:         req.Cmd,
		VolumeBinds: req.VolumeBinds,
		Ports:       ports,
		Resources: &pb.ResourceSpec{
			CpuCores:    req.CPUCores,
			MemoryBytes: req.MemoryBytes,
			GpuCount:    req.GPUCount,
		},
		Name: containerName,
		Pull: req.Pull,
	}
	if err := sess.Send(&pb.MasterMessage{
		Payload: &pb.MasterMessage_CreateContainer{CreateContainer: cmd},
	}); err != nil {
		c.JSON(500, gin.H{"error": "send create: " + err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"container":    cc,
		"node":         gin.H{"id": target.ID, "name": target.Name},
		"external_url": externalURL(c, externalPort),
	})
}

func (s *Server) getContainer(c *gin.Context) {
	cc, err := s.store.GetContainer(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, cc)
}

func (s *Server) listContainers(c *gin.Context) {
	cs, err := s.store.ListContainers(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// enrich with node name
	nodes, _ := s.store.ListNodes(c.Request.Context())
	nodeNames := map[string]string{}
	for _, n := range nodes {
		nodeNames[n.ID] = n.Name
	}
	out := make([]gin.H, 0, len(cs))
	for _, cc := range cs {
		row := gin.H{
			"id":            cc.ID,
			"node_id":       cc.NodeID,
			"node_name":     nodeNames[cc.NodeID],
			"docker_id":     cc.DockerID,
			"name":          cc.Name,
			"image":         cc.Image,
			"state":         cc.State,
			"status":        cc.Status,
			"cpu_cores":     cc.CPUCores,
			"memory_bytes":  cc.MemoryBytes,
			"gpu_count":     cc.GPUCount,
			"external_port": cc.ExternalPort,
			"created_at":    cc.CreatedAt,
			"updated_at":    cc.UpdatedAt,
		}
		if cc.EnvJSON != "" {
			var env []string
			_ = json.Unmarshal([]byte(cc.EnvJSON), &env)
			row["env"] = env
		}
		if cc.CmdJSON != "" {
			var cmd []string
			_ = json.Unmarshal([]byte(cc.CmdJSON), &cmd)
			row["cmd"] = cmd
		}
		if cc.PortsJSON != "" {
			var ports []*pb.PortMapping
			_ = json.Unmarshal([]byte(cc.PortsJSON), &ports)
			row["ports"] = ports
		}
		if cc.ExternalPort != 0 {
			row["external_url"] = externalURL(c, cc.ExternalPort)
		}
		out = append(out, row)
	}
	c.JSON(200, gin.H{"containers": out})
}

// deleteContainer stops + removes the Docker container, then forgets
// about it on the master (releases the proxy port and DB row).
func (s *Server) deleteContainer(c *gin.Context) {
	id := c.Param("id")
	cc, err := s.store.GetContainer(c.Request.Context(), id)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	if err := s.sendStopCommand(c, cc, true); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if cc.ExternalPort != 0 {
		s.proxy.UnregisterRoute(cc.ExternalPort)
		_ = s.store.FreePort(c.Request.Context(), cc.ExternalPort)
	}
	_ = s.store.DeleteContainer(c.Request.Context(), id)
	c.JSON(200, gin.H{"ok": true})
}

// startContainer starts an existing (stopped) Docker container.
func (s *Server) startContainer(c *gin.Context) {
	id := c.Param("id")
	cc, err := s.store.GetContainer(c.Request.Context(), id)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	sess := s.mgr.Get(cc.NodeID)
	if sess == nil {
		c.JSON(400, gin.H{"error": "node offline"})
		return
	}
	if err := sess.Send(&pb.MasterMessage{
		Payload: &pb.MasterMessage_StartContainer{StartContainer: &pb.StartContainerCommand{
			ContainerId: cc.ID,
			DockerId:    cc.DockerID,
		}},
	}); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// Re-bind the proxy port + route if it was released while the
	// container was stopped. The proxy already has the listener
	// bookkeeping, so we re-register the route then ask it to bind.
	if cc.ExternalPort != 0 {
		port := cc.ExternalPort
		var firstContainerPort uint32
		if len(cc.PortsJSON) > 0 {
			var ports []portMappingJSON
			if err := json.Unmarshal([]byte(cc.PortsJSON), &ports); err == nil && len(ports) > 0 {
				firstContainerPort = ports[0].ContainerPort
			}
		}
		s.proxy.RegisterRoute(port, cc.ID, cc.NodeID, firstContainerPort)
		if err := s.proxy.BindPort(s.lifetime, port); err != nil {
			log.Printf("rebind proxy port %d after start failed: %v", port, err)
		}
	}
	c.JSON(200, gin.H{"ok": true})
}

// stopContainer stops a running container but keeps it (and its proxy
// port) around so it can be started again.
func (s *Server) stopContainer(c *gin.Context) {
	id := c.Param("id")
	cc, err := s.store.GetContainer(c.Request.Context(), id)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	if err := s.sendStopCommand(c, cc, false); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// Release the proxy port so the next container can grab it; we re-bind
	// on the next /start call.
	if cc.ExternalPort != 0 {
		s.proxy.UnregisterRoute(cc.ExternalPort)
	}
	c.JSON(200, gin.H{"ok": true})
}

func (s *Server) sendStopCommand(c *gin.Context, cc *store.Container, remove bool) error {
	sess := s.mgr.Get(cc.NodeID)
	if sess == nil {
		return errors.New("node offline")
	}
	return sess.Send(&pb.MasterMessage{
		Payload: &pb.MasterMessage_StopContainer{StopContainer: &pb.StopContainerCommand{
			ContainerId: cc.ID,
			DockerId:    cc.DockerID,
			Remove:      remove,
		}},
	})
}

// ---- WebSocket ----

func (s *Server) websocket(c *gin.Context) {
	conn, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ch := s.mgr.Updates()
	for u := range ch {
		msg := gin.H{
			"node_id": u.NodeID,
			"kind":    u.Kind,
			"at":      u.At,
		}
		if u.Snapshot != nil {
			msg["metrics"] = gin.H{
				"cpu_percent":         u.Snapshot.CPUPercent,
				"cpu_cores":           u.Snapshot.CPUCores,
				"mem_used_bytes":      u.Snapshot.MemUsedBytes,
				"mem_total_bytes":     u.Snapshot.MemTotalBytes,
				"load1":               u.Snapshot.Load1,
				"gpu_count":           u.Snapshot.GpuCount,
				"gpu_mem_used_bytes":  u.Snapshot.GpuMemUsed,
				"gpu_mem_total_bytes": u.Snapshot.GpuMemTotal,
				"gpu_usage_percent":   u.Snapshot.GpuUsageAvg,
			}
		}
		if err := conn.WriteJSON(msg); err != nil {
			return
		}
	}
}
