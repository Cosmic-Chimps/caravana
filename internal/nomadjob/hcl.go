package nomadjob

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// HCL writes job as a Nomad jobspec in HCL2, for exporting an app. header
// becomes "#" comment lines at the top. It covers the fields Translate sets
// (a test fails naming any it would leave out), in a fixed order with map keys
// sorted, so the same job always gives the same file. It is pure.
func HCL(job *api.Job, header string) []byte {
	w := &hclWriter{}
	if header != "" {
		for _, line := range strings.Split(strings.TrimRight(header, "\n"), "\n") {
			w.line(strings.TrimRight("# "+line, " "))
		}
		w.line("")
	}

	id := deref(job.ID)
	w.block("job", []string{id}, func() {
		if name := deref(job.Name); name != "" && name != id {
			w.attr("name", quote(name))
		}
		w.optString("type", job.Type)
		if len(job.Datacenters) > 0 {
			w.attr("datacenters", quoteList(job.Datacenters))
		}
		w.stringMap("meta", job.Meta)
		w.update(job.Update)
		for _, g := range job.TaskGroups {
			w.group(g)
		}
	})
	return []byte(w.b.String())
}

func (w *hclWriter) group(g *api.TaskGroup) {
	w.block("group", []string{deref(g.Name)}, func() {
		w.optInt("count", g.Count)
		w.optDuration("shutdown_delay", g.ShutdownDelay)
		w.update(g.Update)
		for _, n := range g.Networks {
			w.block("network", nil, func() {
				w.optString("mode", &n.Mode)
				for _, p := range n.DynamicPorts {
					w.block("port", []string{p.Label}, func() {
						if p.To != 0 {
							w.attr("to", strconv.Itoa(p.To))
						}
					})
				}
			})
		}
		for _, name := range slices.Sorted(maps.Keys(g.Volumes)) {
			v := g.Volumes[name]
			w.block("volume", []string{name}, func() {
				w.optString("type", &v.Type)
				w.optString("source", &v.Source)
				if v.ReadOnly {
					w.attr("read_only", "true")
				}
				w.optString("access_mode", &v.AccessMode)
				w.optString("attachment_mode", &v.AttachmentMode)
			})
		}
		for _, s := range g.Services {
			w.service(s)
		}
		for _, t := range g.Tasks {
			w.task(t)
		}
	})
}

func (w *hclWriter) service(s *api.Service) {
	w.block("service", nil, func() {
		w.optString("name", &s.Name)
		w.optString("port", &s.PortLabel)
		w.optString("provider", &s.Provider)
		w.optString("address_mode", &s.AddressMode)
		if len(s.Tags) > 0 {
			w.attr("tags", quoteList(s.Tags))
		}
		if len(s.CanaryTags) > 0 {
			w.attr("canary_tags", quoteList(s.CanaryTags))
		}
		for _, c := range s.Checks {
			w.block("check", nil, func() {
				w.optString("name", &c.Name)
				w.optString("type", &c.Type)
				w.optString("path", &c.Path)
				w.optString("port", &c.PortLabel)
				w.optDuration("interval", &c.Interval)
				w.optDuration("timeout", &c.Timeout)
			})
		}
	})
}

func (w *hclWriter) task(t *api.Task) {
	w.block("task", []string{t.Name}, func() {
		w.optString("driver", &t.Driver)
		w.optDuration("kill_timeout", t.KillTimeout)
		if l := t.Lifecycle; l != nil {
			w.block("lifecycle", nil, func() {
				w.optString("hook", &l.Hook)
				w.attr("sidecar", strconv.FormatBool(l.Sidecar))
			})
		}
		w.block("config", nil, func() {
			for _, k := range slices.Sorted(maps.Keys(t.Config)) {
				w.attr(k, configValue(t.Config[k]))
			}
		})
		w.stringMap("env", t.Env)
		if r := t.Resources; r != nil {
			w.block("resources", nil, func() {
				w.optInt("cpu", r.CPU)
				w.optInt("memory", r.MemoryMB)
				w.optInt("memory_max", r.MemoryMaxMB)
			})
		}
		for _, m := range t.VolumeMounts {
			w.block("volume_mount", nil, func() {
				w.optString("volume", m.Volume)
				w.optString("destination", m.Destination)
				w.optBool("read_only", m.ReadOnly)
			})
		}
		for _, tmpl := range t.Templates {
			w.template(tmpl)
		}
	})
}

func (w *hclWriter) template(t *api.Template) {
	w.block("template", nil, func() {
		if data := deref(t.EmbeddedTmpl); data != "" {
			w.attr("data", templateData(data))
		}
		w.optString("destination", t.DestPath)
		w.optBool("env", t.Envvars)
		w.optString("change_mode", t.ChangeMode)
		if cs := t.ChangeScript; cs != nil {
			w.block("change_script", nil, func() {
				w.optString("command", cs.Command)
				if len(cs.Args) > 0 {
					w.attr("args", quoteList(cs.Args))
				}
				w.optDuration("timeout", cs.Timeout)
				w.optBool("fail_on_error", cs.FailOnError)
			})
		}
	})
}

func (w *hclWriter) update(u *api.UpdateStrategy) {
	if u == nil {
		return
	}
	w.block("update", nil, func() {
		w.optInt("max_parallel", u.MaxParallel)
		w.optString("health_check", u.HealthCheck)
		w.optDuration("min_healthy_time", u.MinHealthyTime)
		w.optDuration("healthy_deadline", u.HealthyDeadline)
		w.optDuration("progress_deadline", u.ProgressDeadline)
		w.optInt("canary", u.Canary)
		w.optBool("auto_revert", u.AutoRevert)
		w.optBool("auto_promote", u.AutoPromote)
	})
}

// hclWriter builds the file line by line, two spaces per level.
type hclWriter struct {
	b     strings.Builder
	depth int
}

func (w *hclWriter) line(s string) {
	if s != "" {
		w.b.WriteString(strings.Repeat("  ", w.depth))
	}
	w.b.WriteString(s)
	w.b.WriteByte('\n')
}

func (w *hclWriter) block(name string, labels []string, body func()) {
	head := name
	for _, l := range labels {
		head += " " + quote(l)
	}
	w.line(head + " {")
	w.depth++
	body()
	w.depth--
	w.line("}")
}

// attr writes name = value, value already in HCL syntax. A multi-line value
// (a heredoc) is written as it is, without indenting its lines.
func (w *hclWriter) attr(name, value string) {
	w.line(name + " = " + value)
}

func (w *hclWriter) optString(name string, v *string) {
	if v != nil && *v != "" {
		w.attr(name, quote(*v))
	}
}

func (w *hclWriter) optInt(name string, v *int) {
	if v != nil {
		w.attr(name, strconv.Itoa(*v))
	}
}

func (w *hclWriter) optBool(name string, v *bool) {
	if v != nil {
		w.attr(name, strconv.FormatBool(*v))
	}
}

func (w *hclWriter) optDuration(name string, v *time.Duration) {
	if v != nil && *v != 0 {
		w.attr(name, quote(v.String()))
	}
}

// stringMap writes m as a map attribute (name = { "k" = "v" }), not a block:
// keys like "caravana.app" or env names with dots aren't HCL identifiers,
// and a quoted key is only allowed in a map.
func (w *hclWriter) stringMap(name string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	w.line(name + " = {")
	w.depth++
	for _, k := range slices.Sorted(maps.Keys(m)) {
		w.line(quote(k) + " = " + quote(m[k]))
	}
	w.depth--
	w.line("}")
}

// configValue writes a docker driver option: Translate sets strings and lists
// of strings only.
func configValue(v any) string {
	switch v := v.(type) {
	case string:
		return quote(v)
	case []string:
		return quoteList(v)
	default:
		// The coverage test catches a new type before it reaches an export.
		return quote(fmt.Sprint(v))
	}
}

// templateData writes a template's text as a heredoc when it ends with a
// newline (a heredoc always does), so it stays readable; otherwise quoted.
// Only "${" and "%{" need escaping in a heredoc.
func templateData(s string) string {
	if !strings.HasSuffix(s, "\n") {
		return quote(s)
	}
	marker := "EOT"
	for strings.Contains(s, marker) {
		marker += "_"
	}
	return "<<" + marker + "\n" + escapeTemplate(s) + marker
}

// quote writes s as an HCL2 quoted string. HCL2 reads "${" and "%{" as the
// start of an expression, so they are doubled; the rest is JSON-like escaping.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return escapeTemplate(b.String())
}

func escapeTemplate(s string) string {
	return strings.NewReplacer("${", "$${", "%{", "%%{").Replace(s)
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = quote(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
