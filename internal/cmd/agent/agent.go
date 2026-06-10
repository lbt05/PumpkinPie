// Package agentcmd is the entry point for the agent role.
package agentcmd

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/pumpkinpie/pumpkinpie/internal/agent"
	"github.com/pumpkinpie/pumpkinpie/internal/agent/identity"
)

// Args holds the parsed flags for the agent subcommand.
type Args struct {
	Master   string
	Name     string
	StateDir string
}

// Run starts an agent and blocks until ctx is cancelled or a fatal error
// occurs. args are the flags after `pp agent`.
func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	a := Args{Master: "127.0.0.1:7000", StateDir: defaultStateDir()}
	fs.StringVar(&a.Master, "master", a.Master, "Master gRPC address")
	fs.StringVar(&a.Name, "name", a.Name, "Node name (default = hostname)")
	fs.StringVar(&a.StateDir, "state-dir", a.StateDir, "Directory for persistent agent state (machine-id)")
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

	machineID, err := identity.Load(a.StateDir)
	if err != nil {
		return err
	}
	log.Printf("[agent] machine id %s (state dir %s)", machineID, a.StateDir)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("[agent] received signal, shutting down...")
		cancel()
	}()

	ag, err := agent.New(a.Master, a.Name, machineID)
	if err != nil {
		return err
	}
	if err := ag.Run(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

func defaultStateDir() string {
	if runtime.GOOS == "linux" {
		return "/var/lib/pp-agent"
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pumpkinpie")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "pumpkinpie")
	}
	return ".pp-agent"
}
