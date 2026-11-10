package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

const serverUsage = `usage: caravana server <command> [flags]

commands:
  adopt-first --name <name> --public-key <key> --address <ip>
                          register this machine as the first server of the cluster (the installer
                          runs it); prints the server's node token, once, on standard output and
                          nothing else. Run again, it rotates the token and updates the key and
                          address.

every command takes -data-dir (CARAVANA_DATA_DIR), default "data"
`

// runServer is `caravana server <command>`, run on the host by the installer. Like `caravana admin`
// it opens the store beside a running server and returns the process exit code.
func runServerCommand(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, serverUsage)
		return exitUsage
	}
	command, args := args[0], args[1:]
	if command == "-h" || command == "-help" || command == "--help" || command == "help" {
		fmt.Fprint(stdout, serverUsage)
		return exitOK
	}
	if command != "adopt-first" {
		fmt.Fprintf(stderr, "caravana server: unknown command %q\n\n%s", command, serverUsage)
		return exitUsage
	}

	fs := flag.NewFlagSet("caravana server "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are printed below, once
	dataDir := fs.String("data-dir", envOr(getenv, "CARAVANA_DATA_DIR", "data"), "directory of the SQLite store")
	name := fs.String("name", "", "the server's name")
	publicKey := fs.String("public-key", "", "the server's WireGuard public key")
	address := fs.String("address", "", "the address other servers reach this one at")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, serverUsage)
			return exitOK
		}
		fmt.Fprintf(stderr, "caravana server %s: %v\n", command, err)
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "caravana server %s: unexpected argument %q\n", command, fs.Arg(0))
		return exitUsage
	}
	for _, f := range []struct{ flag, value string }{{"--name", *name}, {"--public-key", *publicKey}, {"--address", *address}} {
		if f.value == "" {
			fmt.Fprintf(stderr, "caravana server %s: %s is required\n", command, f.flag)
			return exitUsage
		}
	}

	st, err := openAdminStore(ctx, *dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "caravana server %s: %v\n", command, err)
		return exitFailed
	}
	defer st.Close()

	token, err := service.AdoptFirstServer(ctx, st, time.Now, *name, *publicKey, *address)
	if err != nil {
		var fieldErr *app.FieldError
		if errors.As(err, &fieldErr) {
			// A flag's value. Never the value itself: the key isn't secret, but an error never quotes input.
			fmt.Fprintf(stderr, "caravana server %s: --%s: %s\n", command, flagOf(fieldErr.Field), fieldErr.Message)
			return exitUsage
		}
		fmt.Fprintf(stderr, "caravana server %s: %v\n", command, err)
		return exitFailed
	}
	fmt.Fprintln(stdout, token)
	return exitOK
}

// flagOf is the flag that carries a field.
func flagOf(field string) string {
	switch field {
	case "publicKey":
		return "public-key"
	default:
		return field
	}
}
