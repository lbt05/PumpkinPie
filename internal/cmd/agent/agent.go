// Package agentcmd is the entry point for the agent role.
package agentcmd

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/pumpkinpie/pumpkinpie/internal/agent"
	"github.com/pumpkinpie/pumpkinpie/internal/agent/config"
	"github.com/pumpkinpie/pumpkinpie/internal/agent/identity"
)

// Run starts an agent and blocks until ctx is cancelled or a fatal
// error occurs. args are the flags after `pp agent` — only `--config`
// is accepted; every other setting lives in pp-agent.yaml.
func Run(ctx context.Context, args []string) error {
	var configPath string
	if v := os.Getenv("PP_CONFIG"); v != "" {
		configPath = v
	}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config":
			if i+1 >= len(args) {
				log.Fatalf("pp agent: --config requires a path argument")
			}
			configPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			configPath = strings.TrimPrefix(args[i], "--config=")
		case args[i] == "-h" || args[i] == "--help":
			log.Print(usage)
			return nil
		default:
			log.Fatalf("pp agent: unknown argument %q (the only flag is --config)", args[i])
		}
	}
	if configPath == "" {
		configPath = config.DefaultPath
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg, loaded, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if loaded {
		log.Printf("[agent] loaded config from %s", configPath)
	} else {
		log.Printf("[agent] no config file at %s, using built-in defaults (copy pp-agent.yaml to customise)", configPath)
	}

	name := cfg.Name
	if name == "" {
		h, err := os.Hostname()
		if err != nil {
			return err
		}
		name = h
	}

	stateDir := cfg.StateDir
	if stateDir == "" {
		stateDir = defaultStateDir()
	}

	sockPath := resolveDockerSock(cfg.DockerSock)

	machineID, err := identity.Load(stateDir)
	if err != nil {
		return err
	}
	log.Printf("[agent] machine id %s (state dir %s, docker sock %s)", machineID, stateDir, sockPath)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("[agent] received signal, shutting down...")
		cancel()
	}()

	ag, err := agent.New(cfg.Master, name, machineID, sockPath, cfg.ContainerPollInterval, cfg.DockerEventsEnabled())
	if err != nil {
		return err
	}
	if err := ag.Run(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

// resolveDockerSock implements the precedence chain so ad-hoc CLI use
// (DOCKER_SOCK=/some/path ./bin/pp agent --config=...) still works:
//   1. cfg.DockerSock from the YAML
//   2. $DOCKER_SOCK  (legacy env, kept for muscle memory)
//   3. $DOCKER_HOST  unix:// only (standard Docker convention)
//   4. /var/run/docker.sock  (Linux built-in)
func resolveDockerSock(fromYAML string) string {
	if fromYAML != "" {
		return fromYAML
	}
	if v := os.Getenv("DOCKER_SOCK"); v != "" {
		return v
	}
	if v := os.Getenv("DOCKER_HOST"); v != "" {
		if strings.HasPrefix(v, "unix://") {
			return strings.TrimPrefix(v, "unix://")
		}
	}
	return "/var/run/docker.sock"
}

// defaultStateDir returns the platform-appropriate directory for
// the agent's machine-id file. Matches the behaviour the previous
// flag-based version used so existing deployments keep working.
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

const usage = `pp agent — run a pumpkinPie node agent

Usage:
  pp agent [--config=path]

The only flag is --config=<path> (default /etc/pp/pp-agent.yaml).
Every other setting — master gRPC address, node name, state
directory, Docker socket — lives in the YAML.

If the YAML is missing, the agent falls back to built-in defaults:
master="pp-master.internal:7000", name=<hostname>, state_dir
platform-aware, docker_sock resolved from DOCKER_SOCK / DOCKER_HOST
env vars then /var/run/docker.sock.

Run 'pp help' for an overview of all subcommands.
`