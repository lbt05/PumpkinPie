// pp is the single binary for pumpkinPie. The first positional argument
// selects the role:
//
//	pp master   run the central control plane (UI + API + gRPC + reverse proxy)
//	pp agent    run a node agent that registers to a master
//	pp version  print version info
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/pumpkinpie/pumpkinpie/internal/cmd/agent"
	"github.com/pumpkinpie/pumpkinpie/internal/cmd/master"
)

const version = "0.1.0"

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
		fmt.Printf("pp %s\n", version)
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
  pp master --http=:8080 --grpc=:7000 --proxy-port=8081 --db=./pp.db
  pp agent  --master=10.0.0.1:7000 --name=node-A
`)
}
