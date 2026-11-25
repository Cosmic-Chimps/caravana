package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/templates"
)

const templateUsage = `usage: caravana template check <dir>
       caravana template bundle [--version <v>] <dir> <outdir>

For the app template catalog's repository (feature 035). <dir> is its source
layout: catalog.json and templates/<name>/{template.json,compose.yaml,logo.svg}.
A template folder may also hold template.v1.json and compose.v1.yaml: a frozen
format-1 variant, which is what catalog.json offers (Caravana 0.22 and older read
only that file); a folder with only those is offered to them alone.

  check    read every template, render it with every combination of its choices
           and its optional inputs, and run it through Caravana's own Compose and
           job translators; a refusal, or a warning, fails it. Exits 1 when any
           template fails.
  bundle   check, then write <outdir>/catalog.json (format 1, the templates old
           hosts can read), <outdir>/catalog.v2.json (format 2, all) and
           <outdir>/SHA256SUMS: what a release publishes. --version sets the
           bundles' version (a catalog version like 2026.11.12) instead of the
           one in catalog.json.
`

// runTemplate is `caravana template ...`. args are the arguments after
// "template".
func runTemplate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, templateUsage)
		return exitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, templateUsage)
		return exitOK
	case "check":
		return templateCheck(args[1:], stdout, stderr)
	case "bundle":
		return templateBundle(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "caravana template: unknown command %q\n\n%s", args[0], templateUsage)
	return exitUsage
}

func templateCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, templateUsage)
		return exitUsage
	}
	if _, _, ok := checkSource(args[0], "", stdout, stderr); !ok {
		return exitFailed
	}
	return exitOK
}

func templateBundle(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("template bundle", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	version := fs.String("version", "", "the bundle's catalog version")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "caravana template bundle: %v\n\n%s", err, templateUsage)
		return exitUsage
	}
	if fs.NArg() != 2 {
		fmt.Fprint(stderr, templateUsage)
		return exitUsage
	}
	dir, outdir := fs.Arg(0), fs.Arg(1)
	bundles, count, ok := checkSource(dir, *version, stdout, stderr)
	if !ok {
		fmt.Fprintln(stderr, "caravana template bundle: nothing written")
		return exitFailed
	}
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		fmt.Fprintf(stderr, "caravana template bundle: %v\n", err)
		return exitFailed
	}
	for name, data := range map[string][]byte{
		templates.BundleV1File: bundles.V1,
		templates.BundleV2File: bundles.V2,
		"SHA256SUMS":           templates.Sums(bundles),
	} {
		if err := os.WriteFile(filepath.Join(outdir, name), data, 0o644); err != nil {
			fmt.Fprintf(stderr, "caravana template bundle: %v\n", err)
			return exitFailed
		}
	}
	fmt.Fprintf(stdout, "wrote %s, %s and SHA256SUMS: %d templates in format 2\n",
		filepath.Join(outdir, templates.BundleV1File), filepath.Join(outdir, templates.BundleV2File), count)
	for _, name := range bundles.OnlyV2 {
		fmt.Fprintf(stdout, "note %s is in %s only: it uses format 2 and has no frozen format-1 variant\n", name, templates.BundleV2File)
	}
	for _, name := range bundles.OnlyV1 {
		fmt.Fprintf(stdout, "note %s is in %s only: it is frozen in format 1\n", name, templates.BundleV1File)
	}
	return exitOK
}

// checkSource reads the source layout in dir and checks every template of both
// bundles, printing one line each. It returns the bundles, the number of
// templates of the format-2 bundle and whether every one passed.
func checkSource(dir, version string, stdout, stderr io.Writer) (templates.Bundles, int, bool) {
	bundles, err := templates.ReadSource(os.DirFS(dir), version)
	if err != nil {
		fmt.Fprintf(stderr, "caravana template: %v\n", err)
		return templates.Bundles{}, 0, false
	}
	allOK := true
	count := 0
	for _, file := range []struct {
		name string
		data []byte
		only bool // list only the templates the other bundle doesn't have
	}{{templates.BundleV2File, bundles.V2, false}, {templates.BundleV1File, bundles.V1, true}} {
		cat, err := templates.ParseCatalog(file.data)
		if err != nil {
			// ReadSource has parsed it already; this is not expected.
			fmt.Fprintf(stderr, "caravana template: %v\n", err)
			return templates.Bundles{}, 0, false
		}
		if !file.only {
			count = len(cat.Templates)
		}
		for _, t := range cat.Templates {
			if file.only && !slices.Contains(bundles.OnlyV1, t.Name) && !isFrozen(dir, t.Name) {
				continue // the same template as the format-2 bundle's, already checked
			}
			if err := templates.Check(t, templates.CheckSettings()); err != nil {
				var tpl *templates.Error
				if errors.As(err, &tpl) {
					fmt.Fprintf(stderr, "FAIL %v\n", err)
				} else {
					fmt.Fprintf(stderr, "FAIL %s: %v\n", t.Name, err)
				}
				allOK = false
				continue
			}
			if file.only {
				fmt.Fprintf(stdout, "ok   %s %s (frozen, format 1)\n", t.Name, t.Version)
			} else {
				fmt.Fprintf(stdout, "ok   %s %s\n", t.Name, t.Version)
			}
		}
	}
	return bundles, count, allOK
}

// isFrozen says the template's folder has a frozen format-1 variant.
func isFrozen(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, "templates", name, "template.v1.json"))
	return err == nil
}
