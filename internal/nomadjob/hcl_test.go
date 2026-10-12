package nomadjob

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Each translator case also has its export, testdata/<case>.golden.nomad.hcl.
// The gate parses an export with Nomad itself and plans it against the live
// job; these files make every change to the output visible in review.
func TestHCL(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Translate(tc.app, tc.deployment, tc.settings)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			got := HCL(res.Job, "Exported by Caravana.\n\nNeeds: Traefik.")
			path := filepath.Join("testdata", tc.name+".golden.nomad.hcl")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("HCL differs from %s (run with -update and review the diff):\n%s", path, got)
			}
		})
	}
}

// hclCovers lists every job field HCL writes, as paths of Go field names
// ("[]" for a slice element, "{}" for a map value, ".key" for a docker
// config option). A field Translate sets that isn't here fails
// TestHCLCoversTranslate, so an export can never drop it silently.
var hclCovers = []string{
	"ID", "Name", "Type", "Datacenters", "Meta",
	"Update.MaxParallel", "Update.HealthCheck", "Update.MinHealthyTime", "Update.HealthyDeadline",
	"Update.ProgressDeadline", "Update.Canary", "Update.AutoRevert", "Update.AutoPromote",
	"TaskGroups[].Name", "TaskGroups[].Count", "TaskGroups[].ShutdownDelay",
	"TaskGroups[].Update.Canary", "TaskGroups[].Update.AutoPromote",
	"TaskGroups[].Networks[].Mode", "TaskGroups[].Networks[].DynamicPorts[].Label", "TaskGroups[].Networks[].DynamicPorts[].To",
	"TaskGroups[].Volumes{}.Name", "TaskGroups[].Volumes{}.Type", "TaskGroups[].Volumes{}.Source",
	"TaskGroups[].Volumes{}.ReadOnly", "TaskGroups[].Volumes{}.AccessMode", "TaskGroups[].Volumes{}.AttachmentMode",
	"TaskGroups[].Services[].Name", "TaskGroups[].Services[].PortLabel", "TaskGroups[].Services[].Provider",
	"TaskGroups[].Services[].AddressMode", "TaskGroups[].Services[].Tags", "TaskGroups[].Services[].CanaryTags",
	"TaskGroups[].Services[].Checks[].Name", "TaskGroups[].Services[].Checks[].Type", "TaskGroups[].Services[].Checks[].Path",
	"TaskGroups[].Services[].Checks[].PortLabel", "TaskGroups[].Services[].Checks[].Interval", "TaskGroups[].Services[].Checks[].Timeout",
	"TaskGroups[].Tasks[].Name", "TaskGroups[].Tasks[].Driver", "TaskGroups[].Tasks[].KillTimeout",
	"TaskGroups[].Tasks[].Lifecycle.Hook", "TaskGroups[].Tasks[].Lifecycle.Sidecar",
	"TaskGroups[].Tasks[].Config.image", "TaskGroups[].Tasks[].Config.command", "TaskGroups[].Tasks[].Config.args",
	"TaskGroups[].Tasks[].Env",
	"TaskGroups[].Tasks[].Resources.CPU", "TaskGroups[].Tasks[].Resources.MemoryMB", "TaskGroups[].Tasks[].Resources.MemoryMaxMB",
	"TaskGroups[].Tasks[].VolumeMounts[].Volume", "TaskGroups[].Tasks[].VolumeMounts[].Destination", "TaskGroups[].Tasks[].VolumeMounts[].ReadOnly",
	"TaskGroups[].Tasks[].Templates[].EmbeddedTmpl", "TaskGroups[].Tasks[].Templates[].DestPath",
	"TaskGroups[].Tasks[].Templates[].Envvars", "TaskGroups[].Tasks[].Templates[].ChangeMode",
	"TaskGroups[].Tasks[].Templates[].ChangeScript.Command", "TaskGroups[].Tasks[].Templates[].ChangeScript.Args",
	"TaskGroups[].Tasks[].Templates[].ChangeScript.Timeout", "TaskGroups[].Tasks[].Templates[].ChangeScript.FailOnError",
}

func TestHCLCoversTranslate(t *testing.T) {
	for _, tc := range goldenCases() {
		res, err := Translate(tc.app, tc.deployment, tc.settings)
		if err != nil {
			t.Fatalf("%s: Translate: %v", tc.name, err)
		}
		for _, p := range setFields(reflect.ValueOf(*res.Job), "") {
			if !slices.Contains(hclCovers, p) {
				t.Errorf("%s: Translate sets %s, which HCL doesn't write", tc.name, p)
			}
		}
	}
}

// setFields lists the paths of v's fields that hold something: a non-nil
// pointer (even to a zero value: an explicit false matters), or a non-zero
// value. Maps of strings count as one field; the docker config counts per key.
func setFields(v reflect.Value, path string) []string {
	join := func(name string) string {
		if path == "" {
			return name
		}
		return path + "." + name
	}
	var out []string
	for i := range v.NumField() {
		f, field := v.Field(i), v.Type().Field(i)
		p := join(field.Name)
		switch f.Kind() {
		case reflect.Pointer:
			if f.IsNil() {
				continue
			}
			if f.Elem().Kind() == reflect.Struct && f.Elem().Type() != reflect.TypeFor[time.Duration]() {
				out = append(out, setFields(f.Elem(), p)...)
			} else {
				out = append(out, p)
			}
		case reflect.Struct:
			out = append(out, setFields(f, p)...)
		case reflect.Slice:
			if f.Len() == 0 {
				continue
			}
			elem := f.Type().Elem()
			if elem.Kind() == reflect.Pointer {
				elem = elem.Elem()
			}
			if elem.Kind() != reflect.Struct {
				out = append(out, p)
				continue
			}
			for j := range f.Len() {
				e := f.Index(j)
				if e.Kind() == reflect.Pointer {
					e = e.Elem()
				}
				out = append(out, setFields(e, p+"[]")...)
			}
		case reflect.Map:
			if f.Len() == 0 {
				continue
			}
			if field.Name == "Config" {
				for _, k := range f.MapKeys() {
					out = append(out, p+"."+k.String())
				}
				continue
			}
			if f.Type().Elem().Kind() == reflect.Pointer {
				for _, k := range f.MapKeys() {
					out = append(out, setFields(f.MapIndex(k).Elem(), p+"{}")...)
				}
				continue
			}
			out = append(out, p)
		default:
			if !f.IsZero() {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func TestQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", `"plain"`},
		{"Host(`a.example.com`) || Host(`b`)", "\"Host(`a.example.com`) || Host(`b`)\""},
		{`say "hi"`, `"say \"hi\""`},
		{`a\.dump$`, `"a\\.dump$"`},
		{"two\nlines\ttab", `"two\nlines\ttab"`},
		{"${NOMAD_META_x} and %{if}", `"$${NOMAD_META_x} and %%{if}"`},
		{"$HOME 50%", `"$HOME 50%"`},
		{"bell\a", `"bell\u0007"`},
	}
	for _, tt := range tests {
		if got := quote(tt.in); got != tt.want {
			t.Errorf("quote(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestTemplateData(t *testing.T) {
	if got := templateData("a ${x}\n"); got != "<<EOT\na $${x}\nEOT" {
		t.Errorf("heredoc = %q", got)
	}
	if got := templateData("EOT inside\n"); !strings.HasPrefix(got, "<<EOT_\n") || !strings.HasSuffix(got, "\nEOT_") {
		t.Errorf("marker inside the text: %q", got)
	}
	if got := templateData("no newline"); got != `"no newline"` {
		t.Errorf("no trailing newline = %q, want a quoted string", got)
	}
}
