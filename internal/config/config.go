// Package config reads Caravana's settings from environment variables and
// command-line flags. A flag wins over its environment variable, which wins
// over the built-in default.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Config is everything the control plane needs to start.
type Config struct {
	// Listen is the HTTP address, host:port, for the API and the pages.
	// Loopback by default.
	Listen string
	// DataDir holds the SQLite store and its lock file.
	DataDir string
	// Settings are passed to the Nomad job translator.
	Settings nomadjob.Settings
	// NomadAddr is the Nomad HTTP API, e.g. "http://127.0.0.1:4646".
	NomadAddr string
	// WebhookListen is the optional webhook-only listener, host:port. It serves
	// nothing but POST /v1/webhooks/{id}, so it can be exposed while the rest of
	// the API stays on Listen. Empty means off.
	WebhookListen string
	// TrustedProxies are the proxies (Traefik) whose X-Forwarded-For and
	// X-Forwarded-Proto headers Caravana believes. Empty by default: nobody.
	TrustedProxies httpx.Proxies
	// NomadToken is the Nomad ACL token. It comes from the environment only:
	// a flag would show it in the process list.
	NomadToken Secret
}

// Secret is a string that never prints. Formatting it with fmt or logging it
// with slog gives "[redacted]"; Reveal returns the value for the one place
// that needs it.
type Secret string

const redacted = "[redacted]"

func (Secret) String() string   { return redacted }
func (Secret) GoString() string { return redacted }

// LogValue makes slog log "[redacted]" instead of the value. slog checks for
// this method (the slog.LogValuer interface) on every value it logs.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON covers the case LogValue can't: slog's JSON handler encodes a
// struct that contains a Secret with encoding/json, field by field.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Reveal returns the secret's value.
func (s Secret) Reveal() string { return string(s) }

// setting describes one setting's environment variable and flag.
type setting struct {
	env, flag string
}

func (s setting) String() string { return s.env + " / -" + s.flag }

var (
	listenSetting       = setting{"CARAVANA_LISTEN", "listen"}
	dataDirSetting      = setting{"CARAVANA_DATA_DIR", "data-dir"}
	entrypointSetting   = setting{"CARAVANA_TRAEFIK_ENTRYPOINT", "traefik-entrypoint"}
	certResolverSetting = setting{"CARAVANA_CERT_RESOLVER", "cert-resolver"}
	cpuSetting          = setting{"CARAVANA_DEFAULT_CPU_MHZ", "default-cpu-mhz"}
	memorySetting       = setting{"CARAVANA_DEFAULT_MEMORY_MB", "default-memory-mb"}
	namesImageSetting   = setting{"CARAVANA_NAMES_IMAGE", "names-image"}
	backupImageSetting  = setting{"CARAVANA_BACKUP_IMAGE", "backup-image"}
	// Nomad's own variable names, the ones `deploy/dev/lab.sh env` prints.
	webhookListenSetting  = setting{"CARAVANA_WEBHOOK_LISTEN", "webhook-listen"}
	nomadAddrSetting      = setting{"NOMAD_ADDR", "nomad-addr"}
	trustedProxiesSetting = setting{"CARAVANA_TRUSTED_PROXIES", "trusted-proxies"}
	nomadTokenEnv         = "NOMAD_TOKEN"
)

// translatorSettings maps nomadjob's field names to our settings, so its
// validation errors name the env var and flag the operator actually sets.
var translatorSettings = map[string]setting{
	"settings.entrypoint":      entrypointSetting,
	"settings.certResolver":    certResolverSetting,
	"settings.defaultCPUMHz":   cpuSetting,
	"settings.defaultMemoryMB": memorySetting,
	"settings.namesImage":      namesImageSetting,
	"settings.backupImage":     backupImageSetting,
}

// Load reads the configuration. args are the command-line arguments without
// the program name; getenv is os.Getenv, or a fake in tests.
func Load(args []string, getenv func(string) string) (Config, error) {
	envOr := func(s setting, def string) string {
		if v := getenv(s.env); v != "" {
			return v
		}
		return def
	}

	fs := flag.NewFlagSet("caravana", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // the caller prints the returned error
	listen := fs.String(listenSetting.flag, envOr(listenSetting, "127.0.0.1:7070"), "HTTP listen address, host:port")
	dataDir := fs.String(dataDirSetting.flag, envOr(dataDirSetting, "data"), "directory for the SQLite store")
	entrypoint := fs.String(entrypointSetting.flag, envOr(entrypointSetting, "websecure"), "Traefik entrypoint for app routers")
	certResolver := fs.String(certResolverSetting.flag, envOr(certResolverSetting, ""), "Traefik certificate resolver; empty means Traefik's default certificate")
	cpu := fs.String(cpuSetting.flag, envOr(cpuSetting, "100"), "default CPU per service, in MHz")
	memory := fs.String(memorySetting.flag, envOr(memorySetting, "64"), "default memory per service, in MB")
	namesImage := fs.String(namesImageSetting.flag, envOr(namesImageSetting, ""), "image of the sidecar that lets an app's services reach each other by name, pinned by digest; empty means the built-in busybox")
	backupImage := fs.String(backupImageSetting.flag, envOr(backupImageSetting, ""), "image that uploads and downloads backups (rclone), pinned by digest; empty means the built-in one")
	webhookListen := fs.String(webhookListenSetting.flag, envOr(webhookListenSetting, ""), "optional webhook-only listener, host:port; empty means off")
	trustedProxies := fs.String(trustedProxiesSetting.flag, envOr(trustedProxiesSetting, ""), "comma-separated IPs or CIDRs of proxies whose X-Forwarded-* headers are trusted; empty means none")
	nomadAddr := fs.String(nomadAddrSetting.flag, envOr(nomadAddrSetting, "http://127.0.0.1:4646"), "Nomad HTTP API address; the token comes from "+nomadTokenEnv)
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("command line: %w", err)
	}

	c := Config{
		Listen:        *listen,
		DataDir:       *dataDir,
		Settings:      nomadjob.Settings{Entrypoint: *entrypoint, CertResolver: *certResolver, NamesImage: *namesImage, BackupImage: *backupImage},
		NomadAddr:     *nomadAddr,
		WebhookListen: *webhookListen,
		NomadToken:    Secret(getenv(nomadTokenEnv)),
	}

	var err error
	if c.Settings.DefaultCPUMHz, err = parseInt(cpuSetting, *cpu); err != nil {
		return Config{}, err
	}
	if c.Settings.DefaultMemoryMB, err = parseInt(memorySetting, *memory); err != nil {
		return Config{}, err
	}
	if err := checkListen(c.Listen); err != nil {
		return Config{}, fmt.Errorf("%s: %w", listenSetting, err)
	}
	if c.WebhookListen != "" {
		if err := checkListen(c.WebhookListen); err != nil {
			return Config{}, fmt.Errorf("%s: %w", webhookListenSetting, err)
		}
		if c.WebhookListen == c.Listen {
			return Config{}, fmt.Errorf("%s: %q is also the main listen address; use another port", webhookListenSetting, c.WebhookListen)
		}
	}
	if c.TrustedProxies, err = httpx.ParseProxies(*trustedProxies); err != nil {
		return Config{}, fmt.Errorf("%s: %w", trustedProxiesSetting, err)
	}
	if c.DataDir == "" {
		return Config{}, fmt.Errorf("%s: is required", dataDirSetting)
	}
	if err := checkNomadAddr(c.NomadAddr); err != nil {
		return Config{}, fmt.Errorf("%s: %w", nomadAddrSetting, err)
	}
	if err := c.Settings.Validate(); err != nil {
		var fe *app.FieldError
		if errors.As(err, &fe) {
			if s, ok := translatorSettings[fe.Field]; ok {
				return Config{}, fmt.Errorf("%s: %s", s, fe.Message)
			}
		}
		return Config{}, err
	}
	return c, nil
}

func parseInt(s setting, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a whole number", s, v)
	}
	return n, nil
}

func checkListen(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q is not host:port", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port %q must be between 1 and 65535", port)
	}
	return nil
}

func checkNomadAddr(addr string) error {
	u, err := url.Parse(addr)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q must be an http:// or https:// URL with a host, e.g. http://127.0.0.1:4646", addr)
	}
	return nil
}
