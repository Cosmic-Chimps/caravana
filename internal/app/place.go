package app

import "strings"

// The project and environment every app lived in before feature 019. An app
// there keeps its bare name as its ID, so nothing that already exists moves.
const (
	DefaultProject     = "default"
	DefaultEnvironment = "production"
)

// placeSep joins the parts of an ID. Names never contain it, so the parts can
// always be read back.
const placeSep = "--"

// Place is where an app lives: a project, an environment of that project and
// the app's name there. It is not part of App: the app document, and with it
// the revision and the Nomad job, know only the ID, which is the Place
// written out by ID.
type Place struct {
	Project     string
	Environment string
	Name        string
}

// ID is the app ID of the place: the bare name in Default's production
// environment, otherwise "<project>--<environment>--<name>". It is the inverse
// of PlaceOf. Every Nomad-facing name (job, router, variables, volumes,
// backups) derives from this one string.
func (p Place) ID() string {
	if p.Project == DefaultProject && p.Environment == DefaultEnvironment {
		return p.Name
	}
	return p.Project + placeSep + p.Environment + placeSep + p.Name
}

// PlaceOf reads the place from an app ID. An ID without "--" is a name in
// Default's production environment. Otherwise it must be exactly three names
// joined by "--". A refusal is a *FieldError on "id".
//
// "default--production--web" is refused too: that place has one ID, the bare
// "web", and two IDs for one place would be two apps fighting over it.
//
// PlaceOf does not limit lengths: the rule that "<id>-<service>" fits in 63
// characters (in ValidateApp) already does, and it names the ID's length.
func PlaceOf(id string) (Place, error) {
	if !strings.Contains(id, placeSep) {
		if err := ValidName("id", id); err != nil {
			return Place{}, err
		}
		return Place{Project: DefaultProject, Environment: DefaultEnvironment, Name: id}, nil
	}

	parts := strings.Split(id, placeSep)
	if len(parts) != 3 {
		return Place{}, NewFieldError("id", "%q must be <project>--<environment>--<name>: three names, none containing %q", id, placeSep)
	}
	p := Place{Project: parts[0], Environment: parts[1], Name: parts[2]}
	for _, part := range []struct{ what, name string }{
		{"project", p.Project}, {"environment", p.Environment}, {"name", p.Name},
	} {
		if !labelRE.MatchString(part.name) || strings.Contains(part.name, placeSep) {
			return Place{}, NewFieldError("id", "the %s part of %q (%q) is not a lowercase DNS label without %q", part.what, id, part.name, placeSep)
		}
	}
	if p.Project == DefaultProject && p.Environment == DefaultEnvironment {
		return Place{}, NewFieldError("id", "%q is written %q (the bare name) in project %q, environment %q", id, p.Name, DefaultProject, DefaultEnvironment)
	}
	return p, nil
}

// ValidName checks a project, environment or app name: a lowercase DNS label
// (a-z, 0-9 and '-', at most 63 characters) that does not contain "--", which
// separates the parts of an ID. The error is a *FieldError on field.
func ValidName(field, s string) error {
	if !labelRE.MatchString(s) {
		return NewFieldError(field, "%q is not a lowercase DNS label (a-z, 0-9 and '-', at most 63 characters)", s)
	}
	if strings.Contains(s, placeSep) {
		return NewFieldError(field, "%q must not contain %q, which separates the parts of an app ID", s, placeSep)
	}
	return nil
}
