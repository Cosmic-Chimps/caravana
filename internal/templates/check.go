package templates

import (
	"embed"
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

// Check proves a template can be installed, without a registry or Nomad: it
// renders the template with its example inputs and fixed stand-ins for the
// generated secrets, translates the Compose file (a warning is an error: a
// template must be clean), runs Nomad's limits with settings, parses every
// image reference and translates the job.
//
// The error names the template and the path of the defect.
func Check(t Template, settings nomadjob.Settings) error {
	fail := func(field, format string, args ...any) error { return defect(t, field, format, args...) }

	inputs, err := t.CheckInputs(t.ExampleInputs())
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
	req, err := t.Render(values)
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
		if svc.Backup == nil || svc.Backup.Engine != app.BackupPostgres {
			continue
		}
		for _, e := range svc.Env {
			if e.Name == "POSTGRES_PASSWORD" {
				return fail("services."+svc.Name+".environment.POSTGRES_PASSWORD",
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
		if err := Check(t, settings); err != nil {
			return err
		}
	}
	return nil
}

// The embedded catalog: the bundle `caravana template bundle` made, and its
// sums. A test checks the sum, the parse and Check on every template.
//
//go:embed catalog/catalog.json catalog/SHA256SUMS
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
		bundle, err := embedded.ReadFile("catalog/catalog.json")
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
