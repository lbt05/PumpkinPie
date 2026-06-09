package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pumpkinpie/pumpkinpie/internal/agent"
)

func main() {
	master := flag.String("master", "127.0.0.1:7000", "Master gRPC address")
	name := flag.String("name", "", "Node name (default = hostname)")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if *name == "" {
		h, err := os.Hostname()
		if err != nil {
			log.Fatalf("hostname: %v", err)
		}
		*name = h
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("agent shutting down...")
		cancel()
	}()

	a, err := agent.New(*master, *name)
	if err != nil {
		log.Fatalf("init agent: %v", err)
	}
	if err := a.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("agent run: %v", err)
	}
}
