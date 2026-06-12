// Package mastercmd is the entry point for the master role.
package mastercmd

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pumpkinpie/pumpkinpie/internal/master/agentmgr"
	"github.com/pumpkinpie/pumpkinpie/internal/master/api"
	"github.com/pumpkinpie/pumpkinpie/internal/master/proxy"
	"github.com/pumpkinpie/pumpkinpie/internal/master/store"
	pb "github.com/pumpkinpie/pumpkinpie/proto/gen"
	"google.golang.org/grpc"
)

// Args holds the parsed flags for the master subcommand.
type Args struct {
	HTTP string
	GRPC string
	DB   string
}

// Run starts the master and blocks until ctx is cancelled or a fatal error
// occurs. flags must be the FlagSet that already has --http/--grpc/--db
// defined (typically flag.CommandLine after Parse).
func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("master", flag.ExitOnError)
	a := Args{
		HTTP: ":8080",
		GRPC: ":7000",
		DB:   "pumpkinpie.db",
	}
	fs.StringVar(&a.HTTP, "http", a.HTTP, "HTTP listen address (UI + REST API)")
	fs.StringVar(&a.GRPC, "grpc", a.GRPC, "gRPC listen address (agents)")
	fs.StringVar(&a.DB, "db", a.DB, "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	st, err := store.Open(a.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	mgr := agentmgr.NewManager(st)

	// gRPC server for agents
	grpcLn, err := net.Listen("tcp", a.GRPC)
	if err != nil {
		return err
	}
	gs := grpc.NewServer()
	pb.RegisterAgentServiceServer(gs, agentmgr.NewGrpcServer(mgr))
	go func() {
		log.Printf("[master] gRPC server listening on %s", a.GRPC)
		if err := gs.Serve(grpcLn); err != nil {
			log.Printf("[master] grpc serve: %v", err)
		}
	}()

	// Reverse proxy server
	px := proxy.New(mgr)

	// Re-bind proxy ports for containers that were known from a previous run.
	if err := rebindExistingProxyRoutes(ctx, st, px); err != nil {
		log.Printf("[master] rebind existing routes: %v", err)
	}

	// HTTP API + frontend
	apiSrv := api.New(ctx, st, mgr, px)
	httpSrv := &http.Server{
		Addr:              a.HTTP,
		Handler:           apiSrv.Engine(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("[master] HTTP server listening on %s", a.HTTP)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[master] http serve: %v", err)
		}
	}()

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-ctx.Done():
	case sig := <-sigCh:
		log.Printf("[master] received %s, shutting down...", sig)
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	gs.GracefulStop()
	return nil
}

// rebindExistingProxyRoutes scans the store for containers with a
// persisted proxy port and re-binds a listener + route on the master so
// the URL keeps working after a master restart.
func rebindExistingProxyRoutes(ctx context.Context, st *store.Store, px *proxy.Server) error {
	cs, err := st.ListContainers(ctx)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if c.ExternalPort == 0 || c.PortsJSON == "" {
			continue
		}
		var ports []struct {
			ContainerPort uint32 `json:"container_port"`
			Protocol      string `json:"protocol"`
			HostPort      uint32 `json:"host_port"`
		}
		if err := json.Unmarshal([]byte(c.PortsJSON), &ports); err != nil || len(ports) == 0 {
			continue
		}
		px.LoadExistingRoute(c.ExternalPort, c.ID, c.NodeID, ports[0].HostPort)
		if err := px.BindPort(ctx, c.ExternalPort); err != nil {
			log.Printf("[master] rebind :%d -> %s: %v", c.ExternalPort, c.ID, err)
		}
	}
	return nil
}
