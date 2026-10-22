package ui

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// editForm is the Configure form: one section per service, with what was
// typed and what is wrong with it (feature 014, data-model "Edit form").
// Revision is the revision of the stored app the form was drawn from; it
// travels in a hidden input and comes back as the deploy's condition.
// Notice is an error that isn't about one field, Focus names the input that
// gets the cursor: the first one with an error.
//
// The form is Copies & size and Shipping (feature 021). A service's environment
// and domains are not in it: they have their own pages (Variables & secrets,
// Domains), so saving this form leaves them as stored.
type editForm struct {
	Revision string
	Services []editService
	Notice   sentence
	Focus    string
}

// editService is the inputs of one service. The formFields hold the text as
// typed (for drawing the form again); Edit holds the same values as the
// model wants them, set only when the text parsed. The Show flags say which
// inputs the service has: a service with several ports has no port input (the
// form can't tell which one to change), and one without ports has no health
// check.
type editService struct {
	Name string
	Edit serviceEdit

	ShowPort, ShowHealth bool

	Image, Port, Copies                       formField
	HealthPath, HealthInterval, HealthTimeout formField
	CPU, Memory, MemoryMax                    formField
}

// serviceEdit is what one save sets on a service: exactly the fields the form
// offers, in the model's types. applyEdit copies them onto the stored service;
// everything else of the service stays as stored.
type serviceEdit struct {
	Image          string
	Port           int    // only meaningful when ShowPort
	Replicas       *int   // nil: the default, one copy
	HealthPath     string // empty: a TCP check
	HealthInterval time.Duration
	HealthTimeout  time.Duration
	CPUMHz         int // zero: the default
	MemoryMB       int
	MemoryMaxMB    int
}

// inputName is the name a service's input has in the posted form, and so in
// errors: svc.<service>.<field>. Service names are DNS labels, so they hold no
// dot and the name can be taken apart again.
func inputName(service, field string) string { return "svc." + service + "." + field }

// inputID is the element ID of the same input (the page's anchors and
// aria-describedby ids can't hold a dot comfortably): svc-<service>-<field>.
func inputID(service, field string) string { return "svc-" + service + "-" + field }

// fields lists the service's inputs in the order they show, with the field
// part of their posted names.
func (s *editService) fields() []struct {
	Name  string
	Field *formField
} {
	return []struct {
		Name  string
		Field *formField
	}{
		{"copies", &s.Copies}, {"cpu", &s.CPU}, {"memory", &s.Memory}, {"memoryMax", &s.MemoryMax},
		{"image", &s.Image}, {"port", &s.Port},
		{"healthPath", &s.HealthPath}, {"healthInterval", &s.HealthInterval}, {"healthTimeout", &s.HealthTimeout},
	}
}

// inputSpec is how one input is drawn: its label above, a prefix or suffix
// inside it ("GET", "MB"), a hint below, the placeholder that says what an empty
// value means, and its shape. All of them are mono, as the handoff has them.
type inputSpec struct {
	Name        string // the field part of the posted name
	Label       string
	Prefix      string
	Suffix      string
	Hint        string
	Placeholder string
	Area        bool // a textarea (the Compose form's)
	Wide        bool // takes the whole row
	Numeric     bool // a number: phones show their number pad
}

// inputSpecs are the inputs of the form, by name (Label is also what the stale
// panel calls the field).
var inputSpecs = []inputSpec{
	{Name: "copies", Numeric: true, Label: "Copies", Placeholder: "1"},
	{Name: "cpu", Numeric: true, Label: "CPU", Suffix: "MHz", Placeholder: "default"},
	{Name: "memory", Numeric: true, Label: "Memory", Suffix: "MB", Placeholder: "default"},
	{Name: "memoryMax", Numeric: true, Label: "Ceiling", Suffix: "MB", Placeholder: "default"},
	{Name: "image", Label: "Image", Wide: true},
	{Name: "port", Numeric: true, Label: "Container port"},
	{Name: "healthPath", Label: "Health check", Prefix: "GET", Placeholder: "TCP port", Hint: "Empty checks the TCP port."},
	{Name: "healthInterval", Numeric: true, Label: "Every", Suffix: "s", Placeholder: "10"},
	{Name: "healthTimeout", Numeric: true, Label: "Timeout", Suffix: "s", Placeholder: "2"},
}

// shows reports whether the service has this input: see the Show flags.
func (s editService) shows(field string) bool {
	switch field {
	case "port":
		return s.ShowPort
	case "healthPath", "healthInterval", "healthTimeout":
		return s.ShowHealth
	}
	return true
}

// editInput is one input of the Compose form's overlay as its page draws it.
type editInput struct {
	inputSpec
	Field *formField
}

// fieldView is one input as the "field" template draws it: the spec and the text
// as typed, with its error.
type fieldView struct {
	inputSpec
	Field   *formField
	Service string
}

// F returns the named input of the service, ready to draw: the "field" template
// takes it ({{template "field" (.F "copies")}}).
func (s editService) F(name string) fieldView {
	for _, in := range s.fields() {
		if in.Name != name {
			continue
		}
		for _, spec := range inputSpecs {
			if spec.Name == name {
				return fieldView{inputSpec: spec, Field: in.Field, Service: s.Name}
			}
		}
	}
	return fieldView{}
}

// InputName is the name the input has in the posted form.
func (f fieldView) InputName() string { return inputName(f.Service, f.Name) }

// newEditService makes a service's section with its IDs set and its Show
// flags worked out from the stored service.
func newEditService(svc app.Service) editService {
	s := editService{
		Name:       svc.Name,
		ShowPort:   len(svc.Ports) == 1,
		ShowHealth: len(svc.Ports) > 0,
	}
	for _, in := range s.fields() {
		in.Field.ID = inputID(svc.Name, in.Name)
	}
	return s
}

// service returns the section for the named service.
func (f *editForm) service(name string) (*editService, bool) {
	for i := range f.Services {
		if f.Services[i].Name == name {
			return &f.Services[i], true
		}
	}
	return nil, false
}

// field returns the input with the given posted name (svc.<service>.<field>).
func (f *editForm) field(name string) (*formField, bool) {
	rest, ok := strings.CutPrefix(name, "svc.")
	if !ok {
		return nil, false
	}
	svcName, field, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, false
	}
	s, ok := f.service(svcName)
	if !ok {
		return nil, false
	}
	for _, in := range s.fields() {
		if in.Name == field {
			return in.Field, true
		}
	}
	return nil, false
}

// markFirstError chooses which error is announced and which input is focused.
func (f *editForm) markFirstError() {
	for i := range f.Services {
		s := &f.Services[i]
		for _, in := range s.fields() {
			if len(in.Field.Error) > 0 {
				in.Field.Alert, f.Focus = true, inputName(s.Name, in.Name)
				return
			}
		}
	}
}

// domainPort is the port name the service's domains go to: the one its
// domains already use, else its only port. ok is false when the service has
// several ports and no domain yet, so a page can't know which port a new
// domain is for.
func domainPort(svc app.Service) (name string, ok bool) {
	switch {
	case len(svc.Domains) > 0:
		return svc.Domains[0].Port, true
	case len(svc.Ports) == 1:
		return svc.Ports[0].Name, true
	}
	return "", false
}

// formOf fills the form from a stored app: the inverse of parseEditForm.
// revision is the stored app's revision.
func formOf(stored app.App, revision string) editForm {
	f := editForm{Revision: revision}
	for _, svc := range stored.Services {
		s := newEditService(svc)
		e := &s.Edit
		e.Image = svc.Image
		if s.ShowPort {
			e.Port = svc.Ports[0].ContainerPort
		}
		if svc.Replicas != nil {
			e.Replicas = new(*svc.Replicas)
		}
		if s.ShowHealth && svc.Health != nil {
			e.HealthPath, e.HealthInterval, e.HealthTimeout = svc.Health.Path, svc.Health.Interval, svc.Health.Timeout
		}
		e.CPUMHz, e.MemoryMB, e.MemoryMaxMB = svc.Resources.CPUMHz, svc.Resources.MemoryMB, svc.Resources.MemoryMaxMB

		s.Image.Value = e.Image
		if s.ShowPort {
			s.Port.Value = strconv.Itoa(e.Port)
		}
		if e.Replicas != nil {
			s.Copies.Value = strconv.Itoa(*e.Replicas)
		}
		s.HealthPath.Value = e.HealthPath
		s.HealthInterval.Value, s.HealthTimeout.Value = secondsText(e.HealthInterval), secondsText(e.HealthTimeout)
		s.CPU.Value, s.Memory.Value, s.MemoryMax.Value = numberText(e.CPUMHz), numberText(e.MemoryMB), numberText(e.MemoryMaxMB)
		f.Services = append(f.Services, s)
	}
	return f
}

// envText writes variables as the .env editor has them: one NAME=value per
// line. A value with a line break can't be written this way; it comes back as a
// line without "=" and is refused, never changed silently.
func envText(env []app.EnvVar) string {
	lines := make([]string, len(env))
	for i, v := range env {
		lines[i] = v.Name + "=" + v.Value
	}
	return strings.Join(lines, "\n")
}

// numberText is a number as an input shows it: nothing for zero, which means
// "the default".
func numberText(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// secondsText is a duration in seconds as an input shows it: whole seconds,
// or a decimal when the stored value isn't whole; nothing for zero.
func secondsText(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// parseEditForm reads the posted form for the stored app's services. It
// always returns the form with every value as typed; ok is false when some
// input has an error, which sits on that input. A service the post says
// nothing about (its image input is absent: another service's form, or a form
// drawn before the service was added) is taken as stored, never as emptied.
func parseEditForm(post url.Values, stored app.App) (editForm, bool) {
	f := formOf(stored, strings.TrimSpace(post.Get("revision")))
	ok := true
	for i, svc := range stored.Services {
		s := &f.Services[i]
		if !post.Has(inputName(svc.Name, "image")) {
			continue
		}
		if !s.parse(post) {
			ok = false
		}
	}
	return f, ok
}

// parse reads one service's inputs from the post, setting Edit and any
// errors, and reports whether every input was good.
func (s *editService) parse(post url.Values) bool {
	ok := true
	refuse := func(field *formField, text string) {
		field.Error, ok = sentence{plain(text)}, false
	}
	text := func(name string) string { return strings.TrimSpace(post.Get(inputName(s.Name, name))) }
	// whole parses an optional whole number from min up; empty is zero.
	whole := func(field *formField, min int, what string) int {
		field.Value = strings.TrimSpace(field.Value)
		if field.Value == "" {
			return 0
		}
		n, err := strconv.Atoi(field.Value)
		if err != nil || n < min {
			refuse(field, what)
			return 0
		}
		return n
	}
	seconds := func(field *formField) time.Duration {
		field.Value = strings.TrimSpace(field.Value)
		if field.Value == "" {
			return 0
		}
		n, err := strconv.ParseFloat(field.Value, 64)
		if err != nil || n <= 0 || n > 3600 {
			refuse(field, "Enter a number of seconds, more than 0.")
			return 0
		}
		return time.Duration(math.Round(n * float64(time.Second)))
	}

	e := &s.Edit
	*e = serviceEdit{}

	s.Image.Value = text("image")
	if s.Image.Value == "" {
		refuse(&s.Image, "Enter an image.")
	}
	e.Image = s.Image.Value

	if s.ShowPort {
		s.Port.Value = text("port")
		if n, err := strconv.Atoi(s.Port.Value); err != nil || n < 1 || n > 65535 {
			refuse(&s.Port, "Enter a number from 1 to 65535.")
		} else {
			e.Port = n
		}
	}

	s.Copies.Value = text("copies")
	if n := whole(&s.Copies, 1, "Enter a whole number, 1 or more."); n > 0 {
		e.Replicas = &n
	}

	if s.ShowHealth {
		s.HealthPath.Value = text("healthPath")
		e.HealthPath = s.HealthPath.Value
		s.HealthInterval.Value, s.HealthTimeout.Value = text("healthInterval"), text("healthTimeout")
		e.HealthInterval, e.HealthTimeout = seconds(&s.HealthInterval), seconds(&s.HealthTimeout)
	}

	s.CPU.Value, s.Memory.Value, s.MemoryMax.Value = text("cpu"), text("memory"), text("memoryMax")
	const notNumber = "Enter a whole number, or leave it empty for the default."
	e.CPUMHz = whole(&s.CPU, 0, notNumber)
	e.MemoryMB = whole(&s.Memory, 0, notNumber)
	e.MemoryMaxMB = whole(&s.MemoryMax, 0, notNumber)
	return ok
}

// parseEnv reads the .env editor: one NAME=value per line, the value being
// everything after the first "=". Blank lines and lines starting with "#" are
// skipped. A line without "=", or with no name, is a problem: a sentence naming
// its line number (counting every line, so it matches the textarea). problem is
// empty when every line is good.
func parseEnv(text string) (env []app.EnvVar, problem string) {
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		switch {
		case !found:
			return env, fmt.Sprintf("Line %d has no \"=\": write it as NAME=value.", i+1)
		case name == "":
			return env, fmt.Sprintf("Line %d has no name before the \"=\".", i+1)
		}
		env = append(env, app.EnvVar{Name: name, Value: value})
	}
	return env, ""
}
