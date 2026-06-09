// Package agentcmd is the entry point for the agent role.
package agentcmd

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pumpkinpie/pumpkinpie/internal/agent"
)

// Args holds the parsed flags for the agent subcommand.
type Args struct {
	Master string
	Name   string
}

// Run starts an agent and blocks until ctx is cancelled or a fatal error
// occurs. args are the flags after `pp agent`.
func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	a := Args{Master: "127.0.0.1:7000"}
	fs.StringVar(&a.Master, "master", a.Master, "Master gRPC address")
	fs.StringVar(&a.Name, "name", a.Name, "Node name (default = hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if a.Name == "" {
		h, err := os.Hostname()
		if err != nil {
			return err
		}
		a.Name = h
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("[agent] received signal, shutting down...")
		cancel()
	}()

	ag, err := agent.New(a.Master, a.Name)
	if err != nil {
		return err
	}
	if err := ag.Run(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}
