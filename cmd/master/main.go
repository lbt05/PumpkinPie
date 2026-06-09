package main

import (
	"context"
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

func main() {
	httpAddr := flag.String("http", ":8080", "HTTP listen address (UI + REST API)")
	grpcAddr := flag.String("grpc", ":7000", "gRPC listen address (agents)")
	proxyPort := flag.Uint("proxy-port", 8081, "External port range start for container proxy")
	dbPath := flag.String("db", "pumpkinpie.db", "SQLite database path")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	mgr := agentmgr.NewManager(st)

	// gRPC server for agents
	grpcLn, err := net.Listen("tcp", *grpcAddr)
	if err != nil {
		log.Fatalf("listen grpc: %v", err)
	}
	gs := grpc.NewServer()
	pb.RegisterAgentServiceServer(gs, agentmgr.NewGrpcServer(mgr))
	go func() {
		log.Printf("gRPC server listening on %s", *grpcAddr)
		if err := gs.Serve(grpcLn); err != nil {
			log.Fatalf("grpc serve: %v", err)
		}
	}()

	// Reverse proxy server
	px := proxy.New(st, mgr)
	if _, err := px.Start(ctx, uint32(*proxyPort)); err != nil {
		log.Fatalf("proxy start: %v", err)
	}

	// HTTP API + frontend
	apiSrv := api.New(st, mgr, px)
	httpSrv := &http.Server{
		Addr:              *httpAddr,
		Handler:           apiSrv.Engine(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("HTTP server listening on %s", *httpAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http serve: %v", err)
		}
	}()

	// Wait for shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("shutting down...")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	gs.GracefulStop()
}
