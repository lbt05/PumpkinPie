// pp is the single binary for pumpkinPie. The first positional argument
// selects the role:
//
//	pp master   run the central control plane (UI + API + gRPC + reverse proxy)
//	pp agent    run a node agent that registers to a master
//	pp version  print version info
//
// Build-time metadata is injected via -ldflags (see Makefile and
// .goreleaser.yml). When built from a plain `go build`, the values
// default to "dev" / "unknown" so the binary still works.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/pumpkinpie/pumpkinpie/internal/buildinfo"
	"github.com/pumpkinpie/pumpkinpie/internal/cmd/agent"
	"github.com/pumpkinpie/pumpkinpie/internal/cmd/master"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	ctx := context.Background()
	var err error
	switch sub {
	case "master":
		err = mastercmd.Run(ctx, args)
	case "agent":
		err = agentcmd.Run(ctx, args)
	case "version", "-v", "--version":
		fmt.Printf("pp %s\n", buildinfo.Version)
		fmt.Printf("  commit:     %s\n", buildinfo.Commit)
		fmt.Printf("  built:      %s\n", buildinfo.BuildTime)
		fmt.Printf("  go version: %s\n", runtime.Version())
		fmt.Printf("  platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "pp: unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "pp %s: %v\n", sub, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `pp - pumpkinPie, multi-node container manager

Usage:
  pp <subcommand> [flags]

Subcommands:
  master     Run the central control plane (UI + REST + gRPC + reverse proxy)
  agent      Run a node agent (registers to a master, hosts containers)
  version    Print version
  help       Print this message

Run 'pp <subcommand> -h' for subcommand-specific flags.

Examples:
  pp master --config=/etc/pp/pp-master.yaml
  pp agent  --config=/etc/pp/pp-agent.yaml
`)
}
