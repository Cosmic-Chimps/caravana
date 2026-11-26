package nomadjob

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Files of a service (feature 060). Each is a Nomad template that writes the file under the task's
// directory plus a read-only docker bind mount of it:
//   - a content file is written to local/caravana-files/<k>, with the content as the template text. The
//     template's delimiters are chosen so neither occurs in the content, which makes Nomad copy it byte for
//     byte ("{{" and "${" in a config file stay as they are);
//   - a secret file is written to secrets/caravana-files/<k> (Nomad's fs API can't read secrets/), and its
//     template reads the one item of the variable the service's secrets are in.
//
// ChangeMode is noop: a new content is a new job version, so a new copy, and a new secret value reaches
// copies only through a deploy, as for the environment.
const (
	contentFilesDir = "local/caravana-files"
	secretFilesDir  = "secrets/caravana-files"
)

// filesDelimiters returns template delimiters that don't occur in content: "[[cv0" and "cv0]]", then
// "[[cv1" and "cv1]]", and so on.
func filesDelimiters(content string) (left, right string) {
	for n := 0; ; n++ {
		left, right = "[[cv"+strconv.Itoa(n), "cv"+strconv.Itoa(n)+"]]"
		if !strings.Contains(content, left) && !strings.Contains(content, right) {
			return left, right
		}
	}
}

// fileTemplates are the templates that write svc's files, one per file in order. secretsPath is the
// variable the service's secrets are read from: the service's own, or the one of a run's job.
func fileTemplates(secretsPath string, svc app.Service) []*api.Template {
	var out []*api.Template
	for k, f := range svc.Files {
		t := &api.Template{
			Perms:      new(f.Perms()),
			Uid:        f.UID,
			Gid:        f.GID,
			ChangeMode: new("noop"),
		}
		if f.Secret != "" {
			t.DestPath = new(fmt.Sprintf("%s/%d", secretFilesDir, k))
			t.EmbeddedTmpl = new(fmt.Sprintf(`{{ with nomadVar %q }}{{ range .Tuples }}{{ if eq .K %q }}{{ printf "%%s" .V }}{{ end }}{{ end }}{{ end }}`,
				secretsPath, f.Secret))
		} else {
			t.DestPath = new(fmt.Sprintf("%s/%d", contentFilesDir, k))
			t.EmbeddedTmpl = new(f.Content)
			left, right := filesDelimiters(f.Content)
			t.LeftDelim, t.RightDelim = new(left), new(right)
		}
		out = append(out, t)
	}
	return out
}

// fileMounts are the docker driver's mount blocks for svc's files: a list of maps, as the JSON job format
// takes a block. Sources are relative to the task directory.
func fileMounts(svc app.Service) []map[string]any {
	var out []map[string]any
	for k, f := range svc.Files {
		dir := contentFilesDir
		if f.Secret != "" {
			dir = secretFilesDir
		}
		out = append(out, map[string]any{
			"type":     "bind",
			"target":   f.Path,
			"source":   fmt.Sprintf("%s/%d", dir, k),
			"readonly": true,
		})
	}
	return out
}

// skipItems is the template text that leaves out the items a service's files hold: wrap a line of a
// range over .Tuples between open and close. Both are empty when there are none, so a service without
// secret files has the template it always had. names are sorted, so the job is stable.
func skipItems(names []string) (begin, end string) {
	if len(names) == 0 {
		return "", ""
	}
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	quoted := make([]string, len(sorted))
	for i, n := range sorted {
		quoted[i] = strconv.Quote(n)
	}
	return `{{ if not (eq .K ` + strings.Join(quoted, " ") + `) }}`, `{{ end }}`
}
