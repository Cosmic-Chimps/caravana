package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Exit codes of `caravana admin`.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2 // unknown command or flag, missing required flag
	exitState  = 3 // the current state doesn't allow it
)

const adminUsage = `usage: caravana admin <command> [flags]

commands:
  init --email <email>                       create the one admin
  status                                     show the admin, if there is one
  reset-password                             set a new password
  create-token --name <name> [--expires-in <duration>]
                                             create an API token (printed on standard output)
  revoke-token --name <name>                 revoke the active token with that name

every command takes -data-dir (CARAVANA_DATA_DIR), default "data"
`

// runAdmin is `caravana admin <command>`, run on the host by the owner. args
// are the arguments after "admin". It works whether the server is running or
// not, and returns the process exit code.
func runAdmin(ctx context.Context, args []string, getenv func(string) string,
	stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return exitUsage
	}
	command, args := args[0], args[1:]
	if command == "-h" || command == "-help" || command == "--help" || command == "help" {
		fmt.Fprint(stdout, adminUsage)
		return exitOK
	}

	// Every command has its own flags, plus -data-dir.
	fs := flag.NewFlagSet("caravana admin "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are printed below, once
	dataDir := fs.String("data-dir", envOr(getenv, "CARAVANA_DATA_DIR", "data"), "directory of the SQLite store")
	var email, name, expiresIn *string
	switch command {
	case "init":
		email = fs.String("email", "", "the admin's email")
	case "status", "reset-password":
	case "create-token":
		name = fs.String("name", "", "a label for the token")
		expiresIn = fs.String("expires-in", "", "how long the token lasts, e.g. 720h; default: never")
	case "revoke-token":
		name = fs.String("name", "", "the name of the token to revoke")
	default:
		fmt.Fprintf(stderr, "caravana admin: unknown command %q\n\n%s", command, adminUsage)
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, adminUsage)
			return exitOK
		}
		fmt.Fprintf(stderr, "caravana admin %s: %v\n", command, err)
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "caravana admin %s: unexpected argument %q\n", command, fs.Arg(0))
		return exitUsage
	}
	if command == "init" && strings.TrimSpace(*email) == "" {
		fmt.Fprintln(stderr, "caravana admin init: --email is required")
		return exitUsage
	}
	if (command == "create-token" || command == "revoke-token") && *name == "" {
		fmt.Fprintf(stderr, "caravana admin %s: --name is required\n", command)
		return exitUsage
	}
	var lifetime time.Duration
	if expiresIn != nil && *expiresIn != "" {
		d, err := time.ParseDuration(*expiresIn)
		if err != nil || d <= 0 {
			fmt.Fprintf(stderr, "caravana admin create-token: --expires-in %q must be a positive duration such as 720h\n", *expiresIn)
			return exitUsage
		}
		lifetime = d
	}

	st, err := openAdminStore(ctx, *dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "caravana admin %s: %v\n", command, err)
		return exitFailed
	}
	defer st.Close()
	// The CLI prints its own results; the security events go to the server's log.
	auth := service.NewAuth(st, slog.New(slog.DiscardHandler), service.AuthOptions{})

	switch command {
	case "init":
		return adminInit(ctx, auth, *email, stdin, stdout, stderr)
	case "status":
		return adminStatus(ctx, auth, stdout, stderr)
	case "reset-password":
		return adminResetPassword(ctx, auth, stdin, stdout, stderr)
	case "create-token":
		return adminCreateToken(ctx, auth, *name, lifetime, stdout, stderr)
	default: // revoke-token
		return adminRevokeToken(ctx, auth, *name, stdout, stderr)
	}
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// openAdminStore opens the data directory the way the server does. If a
// running server holds its lock, it opens the database beside the server
// instead (research R4): SQLite lets both write safely.
func openAdminStore(ctx context.Context, dataDir string) (*store.Store, error) {
	st, err := store.Open(ctx, dataDir)
	if errors.Is(err, store.ErrLocked) {
		return store.OpenShared(ctx, dataDir)
	}
	return st, err
}

func adminInit(ctx context.Context, auth *service.Auth, email string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Refuse before asking for a password the owner would type for nothing.
	if _, err := auth.AdminEmail(ctx); err == nil {
		fmt.Fprintf(stderr, "caravana admin init: %v\n", service.ErrAdminExists)
		return exitState
	} else if !errors.Is(err, service.ErrNoAdmin) {
		fmt.Fprintf(stderr, "caravana admin init: %v\n", err)
		return exitFailed
	}
	password, err := readPassword(stdin, stderr, "Password (12+ characters): ", "Password again: ")
	if err != nil {
		fmt.Fprintf(stderr, "caravana admin init: %v\n", err)
		return exitFailed
	}
	if err := auth.InitAdmin(ctx, email, password); err != nil {
		return reportAdminError(stderr, "init", err)
	}
	shown, _ := auth.AdminEmail(ctx)
	fmt.Fprintf(stdout, "caravana has its admin: %s. Sign in at /login.\n", shown)
	return exitOK
}

func adminStatus(ctx context.Context, auth *service.Auth, stdout, stderr io.Writer) int {
	email, err := auth.AdminEmail(ctx)
	switch {
	case errors.Is(err, service.ErrNoAdmin):
		fmt.Fprintln(stdout, "no admin yet: run caravana admin init --email <email>")
		return exitState
	case err != nil:
		fmt.Fprintf(stderr, "caravana admin status: %v\n", err)
		return exitFailed
	}
	fmt.Fprintf(stdout, "admin: %s\n", email)
	return exitOK
}

func adminResetPassword(ctx context.Context, auth *service.Auth, stdin io.Reader, stdout, stderr io.Writer) int {
	switch _, err := auth.AdminEmail(ctx); {
	case errors.Is(err, service.ErrNoAdmin):
		fmt.Fprintf(stderr, "caravana admin reset-password: %v\n", err)
		return exitState
	case err != nil:
		fmt.Fprintf(stderr, "caravana admin reset-password: %v\n", err)
		return exitFailed
	}
	password, err := readPassword(stdin, stderr, "New password (12+ characters): ", "New password again: ")
	if err != nil {
		fmt.Fprintf(stderr, "caravana admin reset-password: %v\n", err)
		return exitFailed
	}
	if err := auth.ResetPassword(ctx, password); err != nil {
		return reportAdminError(stderr, "reset-password", err)
	}
	fmt.Fprintln(stdout, "Password changed. Every browser was signed out; API tokens still work.")
	return exitOK
}

// adminCreateToken prints only the token on standard output, so a script can
// capture it; the notice goes to standard error.
func adminCreateToken(ctx context.Context, auth *service.Auth, name string, lifetime time.Duration, stdout, stderr io.Writer) int {
	var expiresAt *time.Time
	if lifetime > 0 {
		expiresAt = new(time.Now().Add(lifetime))
	}
	created, err := auth.CreateAPIToken(ctx, name, expiresAt)
	if err != nil {
		return reportAdminError(stderr, "create-token", err)
	}
	fmt.Fprintln(stdout, created.Token)
	if created.Info.ExpiresAt != nil {
		fmt.Fprintf(stderr, "Token %s created; it expires %s. It is shown only now.\n",
			name, created.Info.ExpiresAt.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintf(stderr, "Token %s created; it never expires. It is shown only now.\n", name)
	}
	return exitOK
}

func adminRevokeToken(ctx context.Context, auth *service.Auth, name string, stdout, stderr io.Writer) int {
	if err := auth.RevokeAPITokenByName(ctx, name); errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(stderr, "caravana admin revoke-token: no active token is named %q\n", name)
		return exitFailed
	} else if err != nil {
		fmt.Fprintf(stderr, "caravana admin revoke-token: %v\n", err)
		return exitFailed
	}
	fmt.Fprintf(stdout, "Token %s revoked.\n", name)
	return exitOK
}

// reportAdminError prints err for the owner and picks the exit code: a state
// problem (no admin, already one, name taken) is 3, a bad flag value is 2,
// anything else 1.
func reportAdminError(stderr io.Writer, command string, err error) int {
	var fieldErr *app.FieldError
	switch {
	case errors.Is(err, service.ErrNoAdmin), errors.Is(err, service.ErrAdminExists), errors.Is(err, service.ErrTokenNameTaken):
		fmt.Fprintf(stderr, "caravana admin %s: %v\n", command, err)
		return exitState
	case errors.As(err, &fieldErr):
		fmt.Fprintf(stderr, "caravana admin %s: %s: %s\n", command, fieldErr.Field, fieldErr.Message)
		switch fieldErr.Field {
		case "email", "name", "expiresAt":
			return exitUsage // a flag's value
		}
		return exitFailed
	default:
		fmt.Fprintf(stderr, "caravana admin %s: %v\n", command, err)
		return exitFailed
	}
}

// readPassword reads a password from the owner. On a terminal it prompts
// twice without echo (x/term: C# Console.ReadKey(intercept: true) in a loop);
// otherwise it reads one line from stdin, for scripts. There is no password
// flag: flags show up in shell history and the process list.
func readPassword(stdin io.Reader, stderr io.Writer, prompt, again string) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stderr, prompt)
		first, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		fmt.Fprint(stderr, again)
		second, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		if string(first) != string(second) {
			return "", errors.New("the two passwords differ")
		}
		return string(first), nil
	}

	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", errors.New("no password on standard input (pipe one line, or run this in a terminal)")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
