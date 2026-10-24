package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// env returns a getenv function backed by a map, so tests never read the
// real environment.
func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLoad(t *testing.T) {
	defaults := Config{
		Listen:  "127.0.0.1:7070",
		DataDir: "data",
		Settings: nomadjob.Settings{
			Entrypoint:         "websecure",
			CertResolver:       "",
			DefaultCPUMHz:      100,
			DefaultMemoryMB:    256,
			DefaultMemoryMaxMB: 1024,
		},
		NomadAddr:           "http://127.0.0.1:4646",
		VolumeBackupTimeout: 30 * time.Minute,
		NotifyResolveAfter:  5 * time.Minute,
		EdgeAddress:         "127.0.0.1:443",
	}

	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    func(c *Config) // changes from defaults; nil means defaults
		wantErr []string        // substrings the error must contain; nil means success
	}{
		{name: "defaults"},
		{
			name: "env sets every field",
			env: map[string]string{
				"CARAVANA_LISTEN":                "127.0.0.1:9000",
				"CARAVANA_DATA_DIR":              "/var/lib/caravana",
				"CARAVANA_TRAEFIK_ENTRYPOINT":    "https",
				"CARAVANA_CERT_RESOLVER":         "letsencrypt",
				"CARAVANA_DEFAULT_CPU_MHZ":       "250",
				"CARAVANA_DEFAULT_MEMORY_MB":     "128",
				"CARAVANA_DEFAULT_MEMORY_MAX_MB": "512",
			},
			want: func(c *Config) {
				c.Listen = "127.0.0.1:9000"
				c.DataDir = "/var/lib/caravana"
				c.Settings = nomadjob.Settings{Entrypoint: "https", CertResolver: "letsencrypt", DefaultCPUMHz: 250, DefaultMemoryMB: 128, DefaultMemoryMaxMB: 512}
			},
		},
		{
			name: "flag beats env",
			args: []string{"-listen", "127.0.0.1:9100", "-default-memory-mb", "256"},
			env:  map[string]string{"CARAVANA_LISTEN": "127.0.0.1:9000", "CARAVANA_DEFAULT_MEMORY_MB": "128"},
			want: func(c *Config) {
				c.Listen = "127.0.0.1:9100"
				c.Settings.DefaultMemoryMB = 256
			},
		},
		{name: "listen not host:port", args: []string{"-listen", "nope"}, wantErr: []string{"CARAVANA_LISTEN", "-listen"}},
		{name: "listen port zero", args: []string{"-listen", "127.0.0.1:0"}, wantErr: []string{"CARAVANA_LISTEN", "-listen"}},
		{name: "listen port too high", args: []string{"-listen", "127.0.0.1:99999"}, wantErr: []string{"CARAVANA_LISTEN", "-listen"}},
		{name: "data dir empty", args: []string{"-data-dir", ""}, wantErr: []string{"CARAVANA_DATA_DIR", "-data-dir"}},
		{name: "cpu not a number", env: map[string]string{"CARAVANA_DEFAULT_CPU_MHZ": "abc"}, wantErr: []string{"CARAVANA_DEFAULT_CPU_MHZ", "-default-cpu-mhz"}},
		{name: "memory negative", env: map[string]string{"CARAVANA_DEFAULT_MEMORY_MB": "-5"}, wantErr: []string{"CARAVANA_DEFAULT_MEMORY_MB", "-default-memory-mb"}},
		{name: "memory ceiling below the reservation", env: map[string]string{"CARAVANA_DEFAULT_MEMORY_MAX_MB": "128"}, wantErr: []string{"CARAVANA_DEFAULT_MEMORY_MAX_MB", "-default-memory-max-mb"}},
		{name: "entrypoint list", env: map[string]string{"CARAVANA_TRAEFIK_ENTRYPOINT": "a,b"}, wantErr: []string{"CARAVANA_TRAEFIK_ENTRYPOINT", "-traefik-entrypoint"}},
		{name: "cert resolver with space", env: map[string]string{"CARAVANA_CERT_RESOLVER": "le tls"}, wantErr: []string{"CARAVANA_CERT_RESOLVER", "-cert-resolver"}},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: []string{"-nope"}},

		{
			name: "nomad from env",
			env:  map[string]string{"NOMAD_ADDR": "https://nomad.example:4646", "NOMAD_TOKEN": "tok"},
			want: func(c *Config) {
				c.NomadAddr = "https://nomad.example:4646"
				c.NomadToken = "tok"
			},
		},
		{
			name: "nomad addr flag beats env",
			args: []string{"-nomad-addr", "http://10.0.0.5:4646"},
			env:  map[string]string{"NOMAD_ADDR": "http://127.0.0.1:1"},
			want: func(c *Config) { c.NomadAddr = "http://10.0.0.5:4646" },
		},
		{name: "webhook listener from env", env: map[string]string{"CARAVANA_WEBHOOK_LISTEN": "0.0.0.0:7071"},
			want: func(c *Config) { c.WebhookListen = "0.0.0.0:7071" }},
		{name: "webhook listener flag beats env", args: []string{"-webhook-listen", "127.0.0.1:7072"},
			env: map[string]string{"CARAVANA_WEBHOOK_LISTEN": "127.0.0.1:7071"}, want: func(c *Config) { c.WebhookListen = "127.0.0.1:7072" }},
		{name: "webhook listener invalid", args: []string{"-webhook-listen", "nope"}, wantErr: []string{"CARAVANA_WEBHOOK_LISTEN", "-webhook-listen"}},
		{name: "webhook listener same as main", args: []string{"-webhook-listen", "127.0.0.1:7070"}, wantErr: []string{"CARAVANA_WEBHOOK_LISTEN", "main listen address"}},
		{name: "trusted proxies from env", env: map[string]string{"CARAVANA_TRUSTED_PROXIES": "10.0.0.5, 172.18.0.0/16"},
			want: func(c *Config) {
				c.TrustedProxies = httpx.Proxies{netip.MustParsePrefix("10.0.0.5/32"), netip.MustParsePrefix("172.18.0.0/16")}
			}},
		{name: "trusted proxies flag beats env", args: []string{"-trusted-proxies", "::1"},
			env:  map[string]string{"CARAVANA_TRUSTED_PROXIES": "10.0.0.5"},
			want: func(c *Config) { c.TrustedProxies = httpx.Proxies{netip.MustParsePrefix("::1/128")} }},
		{name: "trusted proxies bad entry", env: map[string]string{"CARAVANA_TRUSTED_PROXIES": "10.0.0.5,traefik"},
			wantErr: []string{"CARAVANA_TRUSTED_PROXIES / -trusted-proxies", `"traefik"`}},
		{name: "names image pinned", env: map[string]string{"CARAVANA_NAMES_IMAGE": "ghcr.io/acme/busybox@sha256:" + strings.Repeat("a", 64)},
			want: func(c *Config) { c.Settings.NamesImage = "ghcr.io/acme/busybox@sha256:" + strings.Repeat("a", 64) }},
		{name: "names image with a tag", args: []string{"-names-image", "busybox:1.37"}, wantErr: []string{"CARAVANA_NAMES_IMAGE / -names-image", "pinned by digest"}},
		{name: "volume backup timeout from env", env: map[string]string{"CARAVANA_VOLUME_BACKUP_TIMEOUT": "2h"},
			want: func(c *Config) { c.VolumeBackupTimeout = 2 * time.Hour }},
		{name: "volume backup timeout flag beats env", args: []string{"-volume-backup-timeout", "45m"},
			env: map[string]string{"CARAVANA_VOLUME_BACKUP_TIMEOUT": "2h"}, want: func(c *Config) { c.VolumeBackupTimeout = 45 * time.Minute }},
		{name: "volume backup timeout not a duration", env: map[string]string{"CARAVANA_VOLUME_BACKUP_TIMEOUT": "30"},
			wantErr: []string{"CARAVANA_VOLUME_BACKUP_TIMEOUT / -volume-backup-timeout", `"30"`}},
		{name: "volume backup timeout under a minute", args: []string{"-volume-backup-timeout", "10s"},
			wantErr: []string{"CARAVANA_VOLUME_BACKUP_TIMEOUT", "at least a minute"}},
		{name: "edge address from env", env: map[string]string{"CARAVANA_EDGE_ADDRESS": "10.0.0.5:8443"},
			want: func(c *Config) { c.EdgeAddress = "10.0.0.5:8443" }},
		{name: "edge address flag beats env", args: []string{"-edge-address", "127.0.0.1:8443"},
			env: map[string]string{"CARAVANA_EDGE_ADDRESS": "10.0.0.5:443"}, want: func(c *Config) { c.EdgeAddress = "127.0.0.1:8443" }},
		{name: "edge address without a port", env: map[string]string{"CARAVANA_EDGE_ADDRESS": "10.0.0.5"},
			wantErr: []string{"CARAVANA_EDGE_ADDRESS / -edge-address", `"10.0.0.5"`}},
		{name: "acme and email from env", env: map[string]string{"CARAVANA_ACME": "staging", "CARAVANA_ACME_EMAIL": " me@example.com "},
			want: func(c *Config) { c.ACME = "staging"; c.ACMEEmail = "me@example.com" }},
		{name: "acme flag beats env", args: []string{"-acme", "none"}, env: map[string]string{"CARAVANA_ACME": "production"},
			want: func(c *Config) { c.ACME = "none" }},
		{name: "acme production", args: []string{"-acme", "production"}, want: func(c *Config) { c.ACME = "production" }},
		{name: "acme unknown", env: map[string]string{"CARAVANA_ACME": "yes"},
			wantErr: []string{"CARAVANA_ACME / -acme", `"yes"`, "production, staging or none"}},
		{name: "public URL from env", env: map[string]string{"CARAVANA_PUBLIC_URL": "https://caravana.example.com"},
			want: func(c *Config) { c.PublicURL = "https://caravana.example.com" }},
		{name: "public URL loses its trailing slash", args: []string{"-public-url", "https://caravana.example.com//"},
			want: func(c *Config) { c.PublicURL = "https://caravana.example.com" }},
		{name: "public URL keeps a path and a port", args: []string{"-public-url", "http://10.0.0.5:7070/caravana/"},
			want: func(c *Config) { c.PublicURL = "http://10.0.0.5:7070/caravana" }},
		{name: "public URL flag beats env", args: []string{"-public-url", "https://b.example.com"},
			env: map[string]string{"CARAVANA_PUBLIC_URL": "https://a.example.com"}, want: func(c *Config) { c.PublicURL = "https://b.example.com" }},
		{name: "public URL without a scheme", env: map[string]string{"CARAVANA_PUBLIC_URL": "caravana.example.com"},
			wantErr: []string{"CARAVANA_PUBLIC_URL / -public-url", `"caravana.example.com"`}},
		{name: "public URL ftp", args: []string{"-public-url", "ftp://caravana.example.com"}, wantErr: []string{"CARAVANA_PUBLIC_URL"}},
		{name: "public URL without a host", args: []string{"-public-url", "https://"}, wantErr: []string{"CARAVANA_PUBLIC_URL"}},
		{name: "public URL with a query", args: []string{"-public-url", "https://caravana.example.com/?a=b"}, wantErr: []string{"CARAVANA_PUBLIC_URL"}},
		{name: "notify resolve-after from env", env: map[string]string{"CARAVANA_NOTIFY_RESOLVE_AFTER": "10s"},
			want: func(c *Config) { c.NotifyResolveAfter = 10 * time.Second }},
		{name: "notify resolve-after flag beats env", args: []string{"-notify-resolve-after", "1h"},
			env: map[string]string{"CARAVANA_NOTIFY_RESOLVE_AFTER": "10s"}, want: func(c *Config) { c.NotifyResolveAfter = time.Hour }},
		{name: "notify resolve-after not a duration", env: map[string]string{"CARAVANA_NOTIFY_RESOLVE_AFTER": "5"},
			wantErr: []string{"CARAVANA_NOTIFY_RESOLVE_AFTER / -notify-resolve-after", `"5"`}},
		{name: "notify resolve-after under 10 seconds", args: []string{"-notify-resolve-after", "9s"},
			wantErr: []string{"CARAVANA_NOTIFY_RESOLVE_AFTER", "at least 10 seconds"}},
		{name: "no token flag", args: []string{"-nomad-token", "x"}, wantErr: []string{"-nomad-token"}},
		{name: "nomad addr without scheme", env: map[string]string{"NOMAD_ADDR": "127.0.0.1:4646"}, wantErr: []string{"NOMAD_ADDR", "-nomad-addr"}},
		{name: "nomad addr ftp", args: []string{"-nomad-addr", "ftp://x"}, wantErr: []string{"NOMAD_ADDR", "-nomad-addr"}},
		{name: "nomad addr without host", args: []string{"-nomad-addr", "http://"}, wantErr: []string{"NOMAD_ADDR", "-nomad-addr"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(tc.args, env(tc.env))
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("Load() = %+v, want an error", got)
				}
				for _, s := range tc.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q does not mention %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			want := defaults
			if tc.want != nil {
				tc.want(&want)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Load() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestTokenNeverPrints formats and logs a config every usual way and checks
// the token appears in none of them (spec FR-021, SC-009).
func TestTokenNeverPrints(t *testing.T) {
	const token = "s3cr3t-token"
	c, err := Load(nil, env(map[string]string{"NOMAD_TOKEN": token}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.NomadToken.Reveal() != token {
		t.Fatalf("Reveal() = %q, want the token", c.NomadToken.Reveal())
	}

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	log.Info("config", "config", c, "token", c.NomadToken)
	log.Info("config", slog.Any("config", c))

	outputs := []string{
		fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c),
		fmt.Sprint(c.NomadToken), fmt.Sprintf("%s %q", c.NomadToken, c.NomadToken),
		logs.String(),
	}
	for _, out := range outputs {
		if strings.Contains(out, token) {
			t.Errorf("token printed: %s", out)
		}
	}
}
