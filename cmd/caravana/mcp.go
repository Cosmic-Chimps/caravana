package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/cosmic-chimps/caravana/internal/mcpserver"
)

const mcpUsage = `usage: caravana mcp [--allow-actions]

A Model Context Protocol server over standard input and output, for coding
agents. It runs where the agent is and calls Caravana's API with your token.

settings (from the environment; there is no flag for the token):
  CARAVANA_URL          the console's address, https://... (http:// only for localhost)
  CARAVANA_TOKEN        an API token (Settings > API tokens), or
  CARAVANA_TOKEN_FILE   a file that holds one

  --allow-actions       also offer the tools that change things (deploy, promote,
                        restart, stop, start, back up); without it, read tools only
`

// runMCP is `caravana mcp`, run by the owner's agent on the owner's machine.
// args are the arguments after "mcp". Standard input and output carry the
// protocol, so nothing else may be written to stdout; the log goes to stderr as
// JSON lines and never holds the token or a tool's answer. It returns the
// process exit code: 0 when the client closes the stream, 2 for a usage error,
// 1 for anything else.
func runMCP(ctx context.Context, args []string, getenv func(string) string,
	stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := mcpserver.ConfigFromEnv(getenv, os.ReadFile, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, mcpUsage)
			return exitOK
		}
		fmt.Fprintf(stderr, "caravana mcp: %v\n\n%s", err, mcpUsage)
		var usage *mcpserver.UsageError
		if errors.As(err, &usage) {
			return exitUsage
		}
		return exitFailed
	}
	cfg.Version = version

	log := slog.New(slog.NewJSONHandler(stderr, nil))
	if err := mcpserver.Run(ctx, cfg, stdin, stdout, log); err != nil {
		log.Error("mcp server stopped", "error", err)
		return exitFailed
	}
	return exitOK
}
