package templates

import (
	"embed"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
)

// checkDigest stands in for a resolved image digest: Check never asks a
// registry, it only proves the translator accepts the app.
const checkDigest = "@sha256:0000000000000000000000000000000000000000000000000000000000000000"

// Check proves a template can be installed, without a registry or Nomad. It
// does the following for every combination of its choice inputs' answers (at
// most 64), once with every optional input empty and once with each filled
// with its example: render the template with the example inputs and fixed
// stand-ins for the generated secrets, the certificates and the hashes, translate the Compose file (a warning is
// an error: a template must be clean), run Nomad's limits with settings, parse
// every image reference and translate the job.
//
// The error names the template and the path of the defect, and, for a template
// with choices, the answers it was rendered with.
func Check(t Template, settings nomadjob.Settings) error {
	if t.Unsupported != "" {
		return nil // this Caravana can't read it; the catalog lists it as unavailable
	}
	for _, c := range checkCases(t) {
		if err := checkOnce(t, settings, c.answers, c.where); err != nil {
			return err
		}
	}
	return nil
}

// checkCase is one set of answers Check renders with, and how it reads in an error.
type checkCase struct {
	answers map[string]string
	where   string
}

// checkCases lists the answers Check renders with: each combination of the
// choice inputs, with the optional inputs empty and, when there are any, filled.
func checkCases(t Template) []checkCase {
	var choices []Input
	var optional []Input
	for _, in := range t.Inputs {
		switch {
		case in.Type == TypeChoice:
			choices = append(choices, in)
		case !in.IsRequired():
			optional = append(optional, in)
		}
	}
	base := t.ExampleInputs()
	for _, in := range choices {
		delete(base, in.Name)
	}
	var cases []checkCase
	var walk func(i int, picked map[string]string, label []string)
	walk = func(i int, picked map[string]string, label []string) {
		if i == len(choices) {
			variants := []struct {
				filled bool
				note   string
			}{{false, "optional inputs empty"}}
			if len(optional) > 0 {
				variants = append(variants, struct {
					filled bool
					note   string
				}{true, "optional inputs filled"})
			}
			for _, v := range variants {
				answers := make(map[string]string, len(base)+len(picked))
				for k, val := range base {
					answers[k] = val
				}
				if !v.filled {
					for _, in := range optional {
						delete(answers, in.Name)
					}
				}
				for k, val := range picked {
					answers[k] = val
				}
				parts := slices.Clone(label)
				if len(optional) > 0 {
					parts = append(parts, v.note)
				}
				where := ""
				if len(parts) > 0 {
					where = "with " + strings.Join(parts, ", ")
				}
				cases = append(cases, checkCase{answers: answers, where: where})
			}
			return
		}
		in := choices[i]
		for _, c := range in.Choices {
			picked[in.Name] = c.Value
			walk(i+1, picked, append(slices.Clone(label), in.Name+"="+c.Value))
			delete(picked, in.Name)
		}
	}
	walk(0, map[string]string{}, nil)
	return cases
}

func checkOnce(t Template, settings nomadjob.Settings, answers map[string]string, where string) error {
	fail := func(field, format string, args ...any) error {
		if where != "" {
			format = "(" + where + ") " + format
		}
		return defect(t, field, format, args...)
	}

	inputs, err := t.CheckInputs(answers)
	if err != nil {
		return fail("inputs", "the examples don't pass: %v", err)
	}
	values := make(map[string]string, len(inputs)+len(t.Generated))
	for k, v := range inputs {
		values[k] = v
	}
	for _, g := range t.Generated {
		// The first character of the alphabet, g.Length times: valid, and
		// the same on every run.
		values[g.Name] = strings.Repeat(alphabets[g.Alphabet][:1], g.Length)
	}
	if t.Certificates != nil {
		// Text where a PEM would be, and a fixed text for a bcrypt hash: no key or hash is made here,
		// so Check stays fast however many cases it renders.
		maps.Copy(values, t.Certificates.standIns())
	}
	req, err := t.render(values, standInHash)
	if err != nil {
		return fail("", "render: %v", err)
	}

	a, warnings, err := compose.Translate(t.AppName, req)
	if err != nil {
		return fail("", "%v", err)
	}
	if len(warnings) > 0 {
		return fail(warnings[0].Field, "the rendered file gives a warning, and a template must not: %s", warnings[0].Message)
	}
	a = a.WithLowercaseHosts()
	if err := nomadjob.Check(a, settings); err != nil {
		return fail("", "%v", compose.InFile(err, a))
	}
	digests := make(map[string]string, len(a.Services))
	for _, svc := range a.Services {
		field := "services." + svc.Name + ".image"
		ref, err := registry.Parse(svc.Image)
		if err != nil {
			return fail(field, "%v", err)
		}
		if ref.IgnoredTag != "" {
			return fail(field, "has a tag and a digest; the tag does nothing")
		}
		if ref.Tag == "latest" {
			return fail(field, "%q has no version tag, or a floating one: a template pins the version it was written for", svc.Image)
		}
		digests[svc.Name] = ref.Name + checkDigest
	}
	for _, svc := range a.Services {
		if svc.Backup == nil {
			continue
		}
		// Each dump engine reads its own password names (postgres, mysql, mongodb).
		names := app.BackupPasswordNames(svc.Backup.Engine)
		for _, e := range svc.Env {
			if slices.Contains(names, e.Name) {
				return fail("services."+svc.Name+".environment."+e.Name,
					"the backed-up database's password is a plain environment variable; make it a secret")
			}
		}
	}
	d := app.Deployment{ID: "check", Trigger: app.TriggerManual, Digests: digests}
	if _, err := nomadjob.Translate(a, d, settings, nil); err != nil {
		return fail("", "%v", compose.InFile(err, a))
	}
	return nil
}

// CheckCatalog runs Check on every template of the catalog and returns the
// first failure.
func CheckCatalog(c Catalog, settings nomadjob.Settings) error {
	for _, t := range c.Templates {
		if t.Unsupported != "" {
			continue
		}
		if err := Check(t, settings); err != nil {
			return err
		}
	}
	return nil
}

// The embedded catalog: the bundle `caravana template bundle` made, and its
// sums. A test checks the sum, the parse and Check on every template.
//
//go:embed catalog/catalog.v2.json catalog/SHA256SUMS
var embedded embed.FS

var (
	embeddedOnce   sync.Once
	embeddedCat    Catalog
	embeddedBundle []byte
)

// Embedded returns the catalog built into this binary and its bundle bytes,
// parsed once. If the embedded bundle can't be read (a build defect the tests
// catch) the catalog is empty.
func Embedded() (Catalog, []byte) {
	embeddedOnce.Do(func() {
		bundle, err := embedded.ReadFile("catalog/" + BundleV2File)
		if err != nil {
			return
		}
		c, err := ParseCatalog(bundle)
		if err != nil {
			return
		}
		embeddedCat, embeddedBundle = c, bundle
	})
	return embeddedCat, embeddedBundle
}

// EmbeddedSums is the SHA256SUMS file of the embedded bundle.
func EmbeddedSums() []byte {
	sums, _ := embedded.ReadFile("catalog/SHA256SUMS")
	return sums
}

// CheckSettings are the installation settings Check is run with by the catalog
// repository's CI and by the embedded catalog's test: the defaults of a Caravana
// install, with its Let's Encrypt resolver.
func CheckSettings() nomadjob.Settings {
	return nomadjob.Settings{
		Entrypoint:         "websecure",
		CertResolver:       "letsencrypt",
		DefaultCPUMHz:      100,
		DefaultMemoryMB:    256,
		DefaultMemoryMaxMB: 1024,
	}
}
