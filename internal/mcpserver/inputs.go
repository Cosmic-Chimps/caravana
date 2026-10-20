package mcpserver

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// The alphabets of the inputs. An app ID is a lowercase DNS label, or the
// three parts of one joined by "--" (app.ValidName plus "--"); the other IDs
// are what Caravana generates. Each is put in a URL path, so these patterns
// are what keeps an agent's input from adding a path segment or a query.
const (
	appIDPattern = `^[a-z0-9-]{1,127}$`
	namePattern  = `^[a-z0-9]+(-[a-z0-9]+)*$`
	svcPattern   = `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	refPattern   = `^[A-Za-z0-9-]{1,64}$`
	imagePattern = `^[^\s]+$`
)

var (
	appIDRE = regexp.MustCompile(appIDPattern)
	nameRE  = regexp.MustCompile(namePattern)
	svcRE   = regexp.MustCompile(svcPattern)
	refRE   = regexp.MustCompile(refPattern)
	imageRE = regexp.MustCompile(imagePattern)
)

// Limits and defaults of the numeric inputs.
const (
	defaultLogLines   = 200
	maxLogLines       = 500
	defaultWaitSecs   = 300
	minWaitSecs       = 10
	maxWaitSecs       = 600
	maxImageLength    = 512
	maxNameLength     = 63
	maxAppIDLength    = 127
	maxServiceLength  = 63
	maxRefLength      = 64
	defaultListCopies = true
)

// The inputs of the tools. Each field's `jsonschema` tag is the description a
// model reads; the constraints (patterns, ranges, defaults) are added to the
// schema by schemaFor, from the table below. Optional fields are pointers or
// `omitempty`.

type noInput struct{}

type projectInput struct {
	Project string `json:"project" jsonschema:"the project's name, like shop"`
}

type appInput struct {
	App string `json:"app" jsonschema:"the app's ID: its name in project default, environment production, else project--environment--name, like shop--staging--web"`
}

type deploymentInput struct {
	App        string `json:"app" jsonschema:"the app's ID"`
	Deployment string `json:"deployment" jsonschema:"the deployment's ID, from list_deployments"`
}

type waitInput struct {
	App            string `json:"app" jsonschema:"the app's ID"`
	Deployment     string `json:"deployment" jsonschema:"the deployment's ID, as deploy_image, redeploy_app, promote_app or list_deployments gave it"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty" jsonschema:"how long to wait, in seconds (10 to 600, default 300)"`
}

type copiesInput struct {
	App   string `json:"app" jsonschema:"the app's ID"`
	Usage *bool  `json:"usage,omitempty" jsonschema:"also read each running copy's CPU and memory (default true)"`
}

type logsInput struct {
	App    string `json:"app" jsonschema:"the app's ID"`
	Copy   string `json:"copy,omitempty" jsonschema:"one copy's ID, from list_copies; default every listed copy"`
	Stream string `json:"stream,omitempty" jsonschema:"stdout, stderr or both (default both)"`
	Lines  int    `json:"lines,omitempty" jsonschema:"how many recent lines (1 to 500, default 200)"`
}

type promotionInput struct {
	App string `json:"app" jsonschema:"the source app's ID"`
	To  string `json:"to" jsonschema:"the environment to promote into, another environment of the same project, like production"`
}

type optionalDeploymentInput struct {
	App        string `json:"app" jsonschema:"the app's ID"`
	Deployment string `json:"deployment,omitempty" jsonschema:"a past deployment's ID; default the app as it is now"`
}

type deployImageInput struct {
	App     string `json:"app" jsonschema:"the app's ID"`
	Image   string `json:"image" jsonschema:"the new image, like ghcr.io/owner/repo:tag"`
	Service string `json:"service,omitempty" jsonschema:"the service to change; needed when the app has more than one"`
}

type backupInput struct {
	App     string `json:"app" jsonschema:"the app's ID"`
	Service string `json:"service" jsonschema:"the service with a backup policy to back up now"`
}

// validator is implemented by the inputs that need more than their schema
// says. The schema is checked by the SDK before a handler runs; this is the
// second check, on the values that go into a URL path, so a bug in a schema
// can't become a request to another route.
type validator interface {
	validate() error
}

func invalidInput(format string, args ...any) error {
	return &toolError{Code: "invalid_input", Text: fmt.Sprintf(format, args...)}
}

func checkApp(s string) error {
	if !appIDRE.MatchString(s) {
		return invalidInput("app must be an app ID: lowercase letters, digits and '-', at most %d characters", maxAppIDLength)
	}
	return nil
}

func checkRef(field, s string) error {
	if !refRE.MatchString(s) {
		return invalidInput("%s must be an ID of letters, digits and '-', at most %d characters", field, maxRefLength)
	}
	return nil
}

func checkName(field, s string) error {
	if len(s) > maxNameLength || !nameRE.MatchString(s) {
		return invalidInput("%s must be a lowercase name (letters a-z, digits and single dashes, at most %d characters)", field, maxNameLength)
	}
	return nil
}

func checkService(field, s string) error {
	if !svcRE.MatchString(s) {
		return invalidInput("%s must be a service name (lowercase letters, digits and '-', at most %d characters)", field, maxServiceLength)
	}
	return nil
}

func (in projectInput) validate() error { return checkName("project", in.Project) }
func (in appInput) validate() error     { return checkApp(in.App) }

func (in deploymentInput) validate() error {
	return firstError(checkApp(in.App), checkRef("deployment", in.Deployment))
}

func (in waitInput) validate() error {
	if in.TimeoutSeconds != 0 && (in.TimeoutSeconds < minWaitSecs || in.TimeoutSeconds > maxWaitSecs) {
		return invalidInput("timeoutSeconds must be from %d to %d", minWaitSecs, maxWaitSecs)
	}
	return firstError(checkApp(in.App), checkRef("deployment", in.Deployment))
}

func (in copiesInput) validate() error { return checkApp(in.App) }

func (in logsInput) validate() error {
	if in.Stream != "" && !slices.Contains([]string{"stdout", "stderr", "both"}, in.Stream) {
		return invalidInput("stream must be stdout, stderr or both")
	}
	if in.Lines != 0 && (in.Lines < 1 || in.Lines > maxLogLines) {
		return invalidInput("lines must be from 1 to %d", maxLogLines)
	}
	err := checkApp(in.App)
	if err == nil && in.Copy != "" {
		err = checkRef("copy", in.Copy)
	}
	return err
}

func (in promotionInput) validate() error {
	return firstError(checkApp(in.App), checkName("to", in.To))
}

func (in optionalDeploymentInput) validate() error {
	err := checkApp(in.App)
	if err == nil && in.Deployment != "" {
		err = checkRef("deployment", in.Deployment)
	}
	return err
}

func (in deployImageInput) validate() error {
	if len(in.Image) > maxImageLength || !imageRE.MatchString(in.Image) {
		return invalidInput("image must be one image reference without spaces, at most %d characters", maxImageLength)
	}
	err := checkApp(in.App)
	if err == nil && in.Service != "" {
		err = checkService("service", in.Service)
	}
	return err
}

func (in backupInput) validate() error {
	return firstError(checkApp(in.App), checkService("service", in.Service))
}

// firstError is the first non-nil error.
func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// propertyRules are the constraints of each input property, by its JSON name:
// what a model is told (and the SDK enforces) beyond the Go type. Kept in one
// table so the schema and the checks above say the same thing.
var propertyRules = map[string]func(*jsonschema.Schema){
	"app":        patternRule(appIDPattern, maxAppIDLength),
	"project":    patternRule(namePattern, maxNameLength),
	"to":         patternRule(namePattern, maxNameLength),
	"service":    patternRule(svcPattern, maxServiceLength),
	"deployment": patternRule(refPattern, maxRefLength),
	"copy":       patternRule(refPattern, maxRefLength),
	"image":      patternRule(imagePattern, maxImageLength),
	"stream": func(s *jsonschema.Schema) {
		s.Enum = []any{"stdout", "stderr", "both"}
	},
	"lines": func(s *jsonschema.Schema) {
		s.Minimum, s.Maximum = ptr(1.0), ptr(float64(maxLogLines))
		s.Default = json.RawMessage(fmt.Sprint(defaultLogLines))
	},
	"timeoutSeconds": func(s *jsonschema.Schema) {
		s.Minimum, s.Maximum = ptr(float64(minWaitSecs)), ptr(float64(maxWaitSecs))
		s.Default = json.RawMessage(fmt.Sprint(defaultWaitSecs))
	},
	"usage": func(s *jsonschema.Schema) {
		s.Default = json.RawMessage(fmt.Sprint(defaultListCopies))
	},
}

func patternRule(pattern string, maxLength int) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) {
		s.Pattern = pattern
		s.MaxLength = ptr(maxLength)
		s.MinLength = ptr(1)
	}
}

func ptr[T any](v T) *T { return &v }

// schemaFor is the input schema of a tool: inferred from the input struct, then
// narrowed by propertyRules. A pointer field infers as "may be null"; the
// tools have no use for null, so the type is made plain.
func schemaFor[In any]() (*jsonschema.Schema, error) {
	s, err := jsonschema.For[In](nil)
	if err != nil {
		return nil, fmt.Errorf("infer the input schema: %w", err)
	}
	for name, prop := range s.Properties {
		if len(prop.Types) == 2 && slices.Contains(prop.Types, "null") {
			for _, t := range prop.Types {
				if t != "null" {
					prop.Type, prop.Types = t, nil
				}
			}
		}
		if rule, ok := propertyRules[name]; ok {
			rule(prop)
		}
	}
	return s, nil
}
