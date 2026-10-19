// Package compose turns a Docker Compose file into Caravana's app model.
//
// It is the only package that imports compose-go, and it is pure: the file
// arrives as bytes, nothing is read from disk or from the environment, and
// the same request always gives the same app and warnings. Every attribute
// of the file is honored, honored with a warning, or refused with an error
// naming its path in the file (constitution IV). The list of what is
// honored is in fields.go.
package compose

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/sirupsen/logrus"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// MaxComposeBytes is the largest file Caravana reads.
const MaxComposeBytes = 512 << 10

// Request is a Compose file and what Compose has no words for, per service.
// The json tags are the API's field names.
type Request struct {
	// Compose is the file, as `docker compose up` would read it.
	Compose string `json:"compose"`
	// Name is the app's display name. Empty means the app ID.
	Name string `json:"name,omitempty"`
	// Services holds, per service of the file (by its name), its domains,
	// health check and secrets.
	Services map[string]Overlay `json:"services,omitempty"`
}

// WithoutSecretValues returns the request with every secret's value removed,
// names kept: what Caravana keeps and shows of a request. Deployed again, the
// secret store still holds the values. The copy has its own Services map and
// Secrets slices, so changing it never changes r. (Domains and the rest are
// shared with r: nothing here writes to them.)
func (r Request) WithoutSecretValues() Request {
	if r.Services == nil {
		return r
	}
	services := make(map[string]Overlay, len(r.Services))
	for name, overlay := range r.Services {
		if len(overlay.Secrets) > 0 {
			names := make([]app.Secret, len(overlay.Secrets))
			for i, s := range overlay.Secrets {
				names[i] = app.Secret{Name: s.Name}
			}
			overlay.Secrets = names
		}
		services[name] = overlay
	}
	r.Services = services
	return r
}

// Overlay is the Caravana-only part of one service.
type Overlay struct {
	// Domains are public hostnames routed over HTTPS to the service.
	Domains []Domain `json:"domains,omitempty"`
	// Health is the check Nomad runs. nil means a TCP check on the first
	// port, when the service has ports.
	Health *Health `json:"health,omitempty"`
	// Secrets are the service's secrets, as in app.Service.
	Secrets []app.Secret `json:"secrets,omitempty"`
	// Backup makes the service a database Caravana backs up, as in
	// app.Service.
	Backup *app.Backup `json:"backup,omitempty"`
}

// Domain routes Host to one container port of the service.
type Domain struct {
	Host string `json:"host"`
	// Port is the container port. Zero is allowed when the service has one.
	Port int `json:"port,omitempty"`
}

// Health is a check Nomad can run: HTTP on Path, or TCP when Path is empty.
type Health struct {
	Path string `json:"path,omitempty"`
	// Port is the container port to check. Zero means the domains' port, or
	// the service's only port.
	Port     int           `json:"port,omitempty"`
	Interval time.Duration `json:"interval,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}

// quiet sends compose-go's log lines (it logs warnings with logrus) nowhere,
// once. Caravana says the same things itself, as errors or warnings with a
// field. Caravana logs with log/slog, so nothing else uses logrus.
var quiet sync.Once

// Translate turns req into the app appID, plus warnings about what changed
// in meaning. Errors are *app.FieldError: Field is a path in the file
// ("services.web.build"), in the request ("services.web.domains[0].port"),
// or "compose" for the file as a whole (bad YAML, an unset variable).
func Translate(appID string, req Request) (app.App, []app.Warning, error) {
	quiet.Do(func() { logrus.SetOutput(io.Discard) })

	if req.Compose == "" {
		return app.App{}, nil, app.NewFieldError("compose", "is required")
	}
	if len(req.Compose) > MaxComposeBytes {
		return app.App{}, nil, app.NewFieldError("compose", "is %d bytes; at most %d are read", len(req.Compose), MaxComposeBytes)
	}

	p, err := load(appID, req.Compose)
	if err != nil {
		return app.App{}, nil, err
	}

	names := make([]string, 0, len(p.Services))
	for name := range p.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range sortedKeys(req.Services) {
		if _, ok := p.Services[name]; !ok {
			return app.App{}, nil, app.NewFieldError("services."+name, "the file has no service %q", name)
		}
	}
	if len(names) == 0 {
		return app.App{}, nil, app.NewFieldError("services", "the file has no services")
	}

	a := app.App{ID: appID, Name: req.Name, Source: app.Source{Kind: app.SourceCompose}}
	warnings := projectWarnings(p)
	for _, name := range names {
		svc, w, err := service(p, p.Services[name], req.Services[name])
		if err != nil {
			return app.App{}, nil, err
		}
		a.Services = append(a.Services, svc)
		warnings = append(warnings, w...)
	}

	if err := app.ValidateApp(a); err != nil {
		return app.App{}, nil, inFile(err, names)
	}
	sort.SliceStable(warnings, func(i, j int) bool {
		if warnings[i].Field != warnings[j].Field {
			return warnings[i].Field < warnings[j].Field
		}
		return warnings[i].Message < warnings[j].Message
	})
	return a, warnings, nil
}

// load parses the file with compose-go: from bytes, with no working
// directory, no environment and no files read (research R1). It checks the
// attributes against the allowlist and refuses the first one outside it.
func load(appID, src string) (*types.Project, error) {
	var unsupported []loader.UnsupportedAttribute
	var unset []string

	details := types.ConfigDetails{
		WorkingDir:  "/nonexistent", // never read with the options below
		ConfigFiles: []types.ConfigFile{{Filename: "compose.yaml", Content: []byte(src)}},
		Environment: types.Mapping{}, // the only source for ${...}: nothing
	}
	p, err := loader.LoadWithContext(context.Background(), details,
		func(o *loader.Options) {
			o.SetProjectName(appID, true)
			o.ResolvePaths = false
			o.SkipResolveEnvironment = true
			o.SkipResolveLabels = true
			o.SkipInclude = true
			o.SkipExtends = true
			o.Interpolate.Substitute = substitute(&unset)
		},
		loader.WithSupportedAttributes(supported, func(found []loader.UnsupportedAttribute) { unsupported = found }))
	if len(unset) > 0 {
		sort.Strings(unset)
		return nil, app.NewFieldError("compose",
			"variable %q has no value: Caravana doesn't read its own environment into a file; give it a default (${%s:-value})", unset[0], unset[0])
	}
	if err != nil {
		return nil, app.NewFieldError("compose", "%s", strings.TrimPrefix(err.Error(), "validating compose.yaml: "))
	}
	if len(unsupported) > 0 {
		return nil, refused(unsupported[0])
	}
	if err := checkDrivers(p); err != nil {
		return nil, err
	}
	return p, nil
}

// substitute is compose-go's own variable substitution, except that it
// records each variable that has no value and no default (in Compose it
// silently becomes ""). Defaults like ${NAME:-value} and the $$ escape work
// as in Compose.
func substitute(unset *[]string) func(string, template.Mapping) (string, error) {
	replace := func(s string, mapping template.Mapping, cfg *template.Config) (string, error) {
		var asked string
		record := func(name string) (string, bool) {
			asked = name
			return mapping(name)
		}
		value, applied, err := template.DefaultReplacementAppliedFunc(s, record, cfg)
		if err == nil && !applied && asked != "" {
			*unset = append(*unset, asked)
		}
		return value, err
	}
	return func(s string, mapping template.Mapping) (string, error) {
		return template.SubstituteWithOptions(s, mapping, template.WithReplacementFunction(replace), template.WithoutLogging)
	}
}

// InFile rewrites the field of a model error about a, an app Translate made,
// to its path in the file: "services[1].backup" becomes "services.db.backup".
// Other errors are returned as they are. The deploy flow uses it for what it
// refuses after translation.
func InFile(err error, a app.App) error {
	names := make([]string, len(a.Services))
	for i, svc := range a.Services {
		names[i] = svc.Name
	}
	return inFile(err, names)
}

// inFile rewrites a model error's field ("services[1].volumes[0].name") to
// the service's path in the file ("services.db.volumes[0].name"). names are
// the services in the app's order.
func inFile(err error, names []string) error {
	fe, ok := err.(*app.FieldError)
	if !ok {
		return err
	}
	var i int
	var rest string
	if n, _ := fmt.Sscanf(fe.Field, "services[%d]", &i); n == 1 && i >= 0 && i < len(names) {
		prefix := fmt.Sprintf("services[%d]", i)
		rest = strings.TrimPrefix(fe.Field, prefix)
		return app.NewFieldError("services."+names[i]+rest, "%s", fe.Message)
	}
	return fe
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
