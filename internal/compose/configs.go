package compose

import (
	"fmt"
	"strconv"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// configHint is what a config that reads a file, an environment variable or somewhere else is told:
// Caravana reads nothing from the user's disk or its own environment.
const configHint = "isn't supported: paste the file's content inline (content: |) or send it in the overlay's files"

// checkConfigs refuses the top-level configs Caravana can't honor, naming configs.<name>.<attr>: only
// inline content is read (feature 060). It runs for every config of the file, used or not.
func checkConfigs(p *types.Project) error {
	for _, name := range sortedKeys(p.Configs) {
		c := p.Configs[name]
		f := "configs." + name
		switch {
		case c.File != "":
			return app.NewFieldError(f+".file", "%s", configHint)
		case c.Environment != "":
			return app.NewFieldError(f+".environment", "%s", configHint)
		case bool(c.External):
			return app.NewFieldError(f+".external", "%s", configHint)
		case c.Content == "":
			return app.NewFieldError(f+".content", "is empty: a config needs content: | with the file's text (or send the file in the overlay's files)")
		}
		if err := app.CheckFileContent(f+".content", c.Content); err != nil {
			return err
		}
	}
	return nil
}

// configFiles maps a service's configs (short form: a name, long form: source, target, uid, gid, mode) to
// content files. The short form's target is /<name>, as in Compose. (compose-go merges two entries with
// the same target into the later one, as Compose does, so a service never has two files at a path here.) Errors name the attribute as written:
// services.web.configs[0].target.
func configFiles(field string, p *types.Project, sc types.ServiceConfig) ([]app.File, error) {
	var out []app.File
	for i, ref := range sc.Configs {
		f := fmt.Sprintf("%s.configs[%d]", field, i)
		cfg, ok := p.Configs[ref.Source]
		if !ok {
			return nil, app.NewFieldError(f+".source", "%q isn't declared under the top-level configs", ref.Source)
		}
		target := ref.Target
		if target == "" {
			target = "/" + ref.Source
		}
		if err := app.CheckFilePath(f+".target", target); err != nil {
			return nil, err
		}

		file := app.File{Path: target, Content: cfg.Content}
		if ref.Mode != nil {
			if *ref.Mode < 0 || *ref.Mode > 0o777 {
				return nil, app.NewFieldError(f+".mode", "%04o isn't a file mode: it is at most 0777", int64(*ref.Mode))
			}
			file.Mode = fmt.Sprintf("%04o", int64(*ref.Mode))
		}
		var err error
		if file.UID, err = configOwner(f+".uid", ref.UID); err != nil {
			return nil, err
		}
		if file.GID, err = configOwner(f+".gid", ref.GID); err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, nil
}

// configOwner reads a uid or gid. Compose writes them as strings and allows names, which Caravana can't
// look up in an image.
func configOwner(field, s string) (*int, error) {
	if s == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, app.NewFieldError(field, "%q must be a number: names can't be looked up in the image", s)
	}
	if n < 0 || n > 65535 {
		return nil, app.NewFieldError(field, "is %d; it is between 0 and 65535", n)
	}
	return &n, nil
}

// overlayFiles are the overlay's files of a service, checked, after the Compose file's own. A path both
// put a file at is refused at the overlay's entry. A file that names a secret needs the overlay to give the
// secret too (the overlay is where secrets are); that is checked with the whole service by app.Validate.
func overlayFiles(field string, ov Overlay, fromFile []app.File) ([]app.File, error) {
	taken := map[string]bool{}
	for _, f := range fromFile {
		taken[f.Path] = true
	}
	for k, f := range ov.Files {
		ff := fmt.Sprintf("%s.files[%d]", field, k)
		if taken[f.Path] {
			return nil, app.NewFieldError(ff+".path", "%s is already a file of the Compose file's configs", f.Path)
		}
		taken[f.Path] = true
	}
	return append(append([]app.File(nil), fromFile...), ov.Files...), nil
}
