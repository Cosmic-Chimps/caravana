package mcpserver

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func files(m map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		if s, ok := m[name]; ok {
			return []byte(s), nil
		}
		return nil, fmt.Errorf("open %s: no such file or directory", name)
	}
}

func TestConfigFromEnv(t *testing.T) {
	const token = "cvat_abcDEF123-_xyz"
	tests := []struct {
		name      string
		env       map[string]string
		files     map[string]string
		args      []string
		want      Config
		wantUsage string // a piece of the usage error; "" when it must work
	}{
		{name: "https and a token", env: map[string]string{"CARAVANA_URL": "https://caravana.example.com", "CARAVANA_TOKEN": token},
			want: Config{URL: "https://caravana.example.com", Token: token}},
		{name: "trailing slash dropped", env: map[string]string{"CARAVANA_URL": "https://caravana.example.com/", "CARAVANA_TOKEN": token},
			want: Config{URL: "https://caravana.example.com", Token: token}},
		{name: "several trailing slashes", env: map[string]string{"CARAVANA_URL": "https://example.com/caravana//", "CARAVANA_TOKEN": token},
			want: Config{URL: "https://example.com/caravana", Token: token}},
		{name: "port kept", env: map[string]string{"CARAVANA_URL": "https://example.com:8443", "CARAVANA_TOKEN": token},
			want: Config{URL: "https://example.com:8443", Token: token}},
		{name: "http localhost", env: map[string]string{"CARAVANA_URL": "http://localhost:7070", "CARAVANA_TOKEN": token},
			want: Config{URL: "http://localhost:7070", Token: token}},
		{name: "http 127.0.0.1", env: map[string]string{"CARAVANA_URL": "http://127.0.0.1:7070/", "CARAVANA_TOKEN": token},
			want: Config{URL: "http://127.0.0.1:7070", Token: token}},
		{name: "http 127.0.0.2", env: map[string]string{"CARAVANA_URL": "http://127.0.0.2:7070", "CARAVANA_TOKEN": token},
			want: Config{URL: "http://127.0.0.2:7070", Token: token}},
		{name: "http ::1", env: map[string]string{"CARAVANA_URL": "http://[::1]:7070", "CARAVANA_TOKEN": token},
			want: Config{URL: "http://[::1]:7070", Token: token}},
		{name: "the flag", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": token},
			args: []string{"--allow-actions"}, want: Config{URL: "https://example.com", Token: token, AllowActions: true}},
		{name: "the flag with one dash", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": token},
			args: []string{"-allow-actions"}, want: Config{URL: "https://example.com", Token: token, AllowActions: true}},
		{name: "token file, trimmed", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN_FILE": "/run/tok"},
			files: map[string]string{"/run/tok": "  " + token + " \n"}, want: Config{URL: "https://example.com", Token: token}},
		{name: "token env, trimmed", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": " " + token + "\n"},
			want: Config{URL: "https://example.com", Token: token}},

		{name: "no address", env: map[string]string{"CARAVANA_TOKEN": token}, wantUsage: "CARAVANA_URL is required"},
		{name: "no token", env: map[string]string{"CARAVANA_URL": "https://example.com"}, wantUsage: "CARAVANA_TOKEN or CARAVANA_TOKEN_FILE is required"},
		{name: "both token settings", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": token, "CARAVANA_TOKEN_FILE": "/run/tok"},
			files: map[string]string{"/run/tok": token}, wantUsage: "only one of CARAVANA_TOKEN and CARAVANA_TOKEN_FILE"},
		{name: "http to a remote host", env: map[string]string{"CARAVANA_URL": "http://caravana.example.com", "CARAVANA_TOKEN": token},
			wantUsage: "token would travel in clear"},
		{name: "http to a lookalike of localhost", env: map[string]string{"CARAVANA_URL": "http://localhost.example.com", "CARAVANA_TOKEN": token},
			wantUsage: "token would travel in clear"},
		{name: "http to a private address", env: map[string]string{"CARAVANA_URL": "http://10.0.0.5", "CARAVANA_TOKEN": token},
			wantUsage: "token would travel in clear"},
		{name: "another scheme", env: map[string]string{"CARAVANA_URL": "ftp://example.com", "CARAVANA_TOKEN": token}, wantUsage: "must start with https://"},
		{name: "no scheme", env: map[string]string{"CARAVANA_URL": "caravana.example.com", "CARAVANA_TOKEN": token}, wantUsage: "CARAVANA_URL"},
		{name: "address with a password", env: map[string]string{"CARAVANA_URL": "https://admin:hunter2@example.com", "CARAVANA_TOKEN": token},
			wantUsage: "must not hold a user name or password"},
		{name: "address with a query", env: map[string]string{"CARAVANA_URL": "https://example.com/?token=x", "CARAVANA_TOKEN": token},
			wantUsage: "without a query or fragment"},
		{name: "token with a space inside", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": "cvat_a b"},
			wantUsage: "characters or a length a Caravana token never has"},
		{name: "token with a newline inside", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": "cvat_a\nX-Evil: 1"},
			wantUsage: "characters or a length"},
		{name: "token with a dot", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": "cvat_a.b"},
			wantUsage: "characters or a length"},
		{name: "token too long", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": strings.Repeat("a", 300)},
			wantUsage: "characters or a length"},
		{name: "token file missing", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN_FILE": "/nope"},
			wantUsage: "CARAVANA_TOKEN_FILE: open /nope"},
		{name: "token file empty", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN_FILE": "/run/tok"},
			files: map[string]string{"/run/tok": " \n"}, wantUsage: "the file is empty"},
		{name: "unknown flag", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": token},
			args: []string{"--token", "x"}, wantUsage: "flag provided but not defined"},
		{name: "extra argument", env: map[string]string{"CARAVANA_URL": "https://example.com", "CARAVANA_TOKEN": token},
			args: []string{"now"}, wantUsage: `unexpected argument "now"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConfigFromEnv(env(tt.env), files(tt.files), tt.args)
			if tt.wantUsage != "" {
				var ue *UsageError
				if !errors.As(err, &ue) {
					t.Fatalf("error = %v (%T), want a *UsageError", err, err)
				}
				if !strings.Contains(err.Error(), tt.wantUsage) {
					t.Errorf("error = %q, want it to hold %q", err, tt.wantUsage)
				}
				// The values the owner typed are never echoed.
				for _, secret := range []string{"hunter2", "X-Evil", "cvat_a b", token} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("error %q echoes %q", err, secret)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != tt.want {
				t.Errorf("config = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestConfigHelp(t *testing.T) {
	_, err := ConfigFromEnv(env(nil), files(nil), []string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Errorf("error = %v, want flag.ErrHelp", err)
	}
}
