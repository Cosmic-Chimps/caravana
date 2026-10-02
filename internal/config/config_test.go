package config

import (
	"strings"
	"testing"

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
			Entrypoint:      "websecure",
			CertResolver:    "",
			DefaultCPUMHz:   100,
			DefaultMemoryMB: 64,
		},
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
				"CARAVANA_LISTEN":             "127.0.0.1:9000",
				"CARAVANA_DATA_DIR":           "/var/lib/caravana",
				"CARAVANA_TRAEFIK_ENTRYPOINT": "https",
				"CARAVANA_CERT_RESOLVER":      "letsencrypt",
				"CARAVANA_DEFAULT_CPU_MHZ":    "250",
				"CARAVANA_DEFAULT_MEMORY_MB":  "128",
			},
			want: func(c *Config) {
				c.Listen = "127.0.0.1:9000"
				c.DataDir = "/var/lib/caravana"
				c.Settings = nomadjob.Settings{Entrypoint: "https", CertResolver: "letsencrypt", DefaultCPUMHz: 250, DefaultMemoryMB: 128}
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
		{name: "entrypoint list", env: map[string]string{"CARAVANA_TRAEFIK_ENTRYPOINT": "a,b"}, wantErr: []string{"CARAVANA_TRAEFIK_ENTRYPOINT", "-traefik-entrypoint"}},
		{name: "cert resolver with space", env: map[string]string{"CARAVANA_CERT_RESOLVER": "le tls"}, wantErr: []string{"CARAVANA_CERT_RESOLVER", "-cert-resolver"}},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: []string{"-nope"}},
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
			if got != want {
				t.Errorf("Load() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestLoopback(t *testing.T) {
	tests := []struct {
		listen string
		want   bool
	}{
		{"127.0.0.1:7070", true},
		{"localhost:7070", true},
		{"[::1]:7070", true},
		{":7070", false},
		{"0.0.0.0:7070", false},
		{"192.168.1.10:7070", false},
		{"example.com:7070", false},
	}
	for _, tc := range tests {
		t.Run(tc.listen, func(t *testing.T) {
			if got := (Config{Listen: tc.listen}).Loopback(); got != tc.want {
				t.Errorf("Loopback() = %v, want %v", got, tc.want)
			}
		})
	}
}
