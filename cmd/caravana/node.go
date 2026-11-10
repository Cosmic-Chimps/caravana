package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"

	"github.com/cosmic-chimps/caravana/internal/nodeagent"
)

const nodeUsage = `usage: caravana node <command> [flags]

The agent that runs as root on every Caravana server (feature 031). The installer and a timer run it;
you rarely need to.

commands:
  init-first --address ADDR [--cpu-total-compute N]
                    set up the first server: keys, the cluster's CA, Nomad's config, the network
  join --url URL --token TOKEN [--address ADDR] [--insecure] [--cpu-total-compute N]
                    join this server to a cluster with a token made in the console
  up                make the private network from what the agent last applied (no network needed)
  sync [--renew] [--checks]
                    ask the console for this server's state and apply it; --renew renews the
                    Nomad certificate now; --checks prints the checks the console reports
  show FIELD        print public-key, name, index, role or first
  leave             take this server out of the cluster (not the first server)

Run it as root. CARAVANA_INSECURE=1 (or --insecure) skips checking the console's certificate.
`

// geteuid is os.Geteuid; tests replace it.
var geteuid = os.Geteuid

// runNode is `caravana node <command>`. args are the arguments after "node". It returns the process
// exit code: 0 done (and also when a sync couldn't reach the console, which changes nothing), 1 failed,
// 2 a usage error. It never prints the join token, a node token or a key.
func runNode(ctx context.Context, args []string, getenv func(string) string,
	stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, nodeUsage)
		return exitUsage
	}
	command, args := args[0], args[1:]
	if command == "-h" || command == "-help" || command == "--help" || command == "help" {
		fmt.Fprint(stdout, nodeUsage)
		return exitOK
	}

	fs := flag.NewFlagSet("caravana node "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are printed below, once
	var (
		address, url, token *string
		insecure, renew     *bool
		checks              *bool
		cpu                 *int
	)
	switch command {
	case "init-first":
		address = fs.String("address", "", "the address other servers reach this one at")
		cpu = fs.Int("cpu-total-compute", 0, "Nomad's cpu_total_compute, for hosts that report no clock speed")
	case "join":
		url = fs.String("url", "", "the console's address")
		token = fs.String("token", "", "the join token")
		address = fs.String("address", "", "the address other servers reach this one at")
		insecure = fs.Bool("insecure", false, "don't verify the console's certificate")
		cpu = fs.Int("cpu-total-compute", 0, "Nomad's cpu_total_compute")
	case "sync":
		renew = fs.Bool("renew", false, "renew the Nomad certificate now")
		checks = fs.Bool("checks", false, "print the checks the console reports")
	case "up", "show", "leave":
	default:
		fmt.Fprintf(stderr, "caravana node: unknown command %q\n\n%s", command, nodeUsage)
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, nodeUsage)
			return exitOK
		}
		fmt.Fprintf(stderr, "caravana node %s: %v\n", command, err)
		return exitUsage
	}
	if command == "show" {
		if fs.NArg() != 1 {
			fmt.Fprintf(stderr, "caravana node show: one field is needed: %v\n", nodeagent.ShowFields)
			return exitUsage
		}
	} else if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "caravana node %s: unexpected argument %q\n", command, fs.Arg(0))
		return exitUsage
	}
	if cpu != nil && *cpu < 0 {
		fmt.Fprintf(stderr, "caravana node %s: --cpu-total-compute can't be negative\n", command)
		return exitUsage
	}

	// Every path is under root: "/" on a server, a folder in tests (CARAVANA_NODE_ROOT, which also
	// lifts the check for root, since a test can't be root).
	root := getenv("CARAVANA_NODE_ROOT")
	if root == "" {
		if geteuid() != 0 {
			fmt.Fprintln(stderr, "caravana node: run it as root (sudo)")
			return exitFailed
		}
		root = "/"
	}
	agent := &nodeagent.Agent{
		Root:    root,
		Runner:  nodeagent.ExecRunner{},
		Version: version,
		Stdout:  stdout,
		Stderr:  stderr,
	}
	env, joined, err := agent.LoadNodeEnv()
	if err != nil {
		fmt.Fprintf(stderr, "caravana node %s: read node.env: %v\n", command, err)
		return exitFailed
	}
	// A call to the console doesn't verify its certificate only when the owner said so: the flag or
	// CARAVANA_INSECURE=1 at join, then what node.env recorded.
	skipVerify := getenv("CARAVANA_INSECURE") == "1" || (joined && env.Insecure) || (insecure != nil && *insecure)
	agent.HTTP = nodeagent.NewHTTPClient(skipVerify)

	switch command {
	case "init-first":
		if _, err := netip.ParseAddr(*address); err != nil {
			fmt.Fprintln(stderr, "caravana node init-first: --address is required, an IPv4 or IPv6 address")
			return exitUsage
		}
		err = agent.InitFirst(ctx, nodeagent.InitFirstOptions{Address: *address, CPUTotalCompute: *cpu})
	case "join":
		if !joined { // a server that joined already needs no options: join renders and runs up
			if *url == "" || *token == "" {
				fmt.Fprintln(stderr, "caravana node join: --url and --token are required")
				return exitUsage
			}
			if err := nodeagent.ValidConsoleURL(*url); err != nil {
				fmt.Fprintf(stderr, "caravana node join: --url: %v\n", err)
				return exitUsage
			}
			if !nodeagent.ValidJoinToken(*token) {
				fmt.Fprintln(stderr, "caravana node join: --token isn't a join token: it is cvjn_ and 43 characters")
				return exitUsage
			}
			if *address != "" {
				if _, err := netip.ParseAddr(*address); err != nil {
					fmt.Fprintln(stderr, "caravana node join: --address must be an IPv4 or IPv6 address")
					return exitUsage
				}
			}
		}
		err = agent.Join(ctx, nodeagent.JoinOptions{
			URL: *url, Token: *token, Address: *address, Insecure: skipVerify, CPUTotalCompute: *cpu,
		})
	case "up":
		err = agent.Up(ctx)
	case "sync":
		err = agent.Sync(ctx, nodeagent.SyncOptions{Renew: *renew, PrintChecks: *checks})
	case "show":
		var value string
		if value, err = agent.Show(fs.Arg(0)); err == nil {
			fmt.Fprintln(stdout, value)
		}
	case "leave":
		err = agent.Leave(ctx)
	}
	if err != nil {
		fmt.Fprintf(stderr, "caravana node %s: %v\n", command, err)
		return exitFailed
	}
	return exitOK
}
