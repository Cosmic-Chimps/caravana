package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cosmic-chimps/caravana/internal/templates"
)

const templateUsage = `usage: caravana template check <dir>
       caravana template bundle [--version <v>] <dir> <outdir>

For the app template catalog's repository (feature 035). <dir> is its source
layout: catalog.json and templates/<name>/{template.json,compose.yaml,logo.svg}.

  check    read every template, render it with its example inputs and run it
           through Caravana's own Compose and job translators; a refusal, or a
           warning, fails it. Exits 1 when any template fails.
  bundle   check, then write <outdir>/catalog.json and <outdir>/SHA256SUMS,
           what a release publishes and what Caravana embeds. --version sets
           the bundle's version (a catalog version like 2026.11.12) instead of
           the one in catalog.json.
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
	bundle, count, ok := checkSource(dir, *version, stdout, stderr)
	if !ok {
		fmt.Fprintln(stderr, "caravana template bundle: nothing written")
		return exitFailed
	}
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		fmt.Fprintf(stderr, "caravana template bundle: %v\n", err)
		return exitFailed
	}
	for name, data := range map[string][]byte{"catalog.json": bundle, "SHA256SUMS": templates.Sums(bundle)} {
		if err := os.WriteFile(filepath.Join(outdir, name), data, 0o644); err != nil {
			fmt.Fprintf(stderr, "caravana template bundle: %v\n", err)
			return exitFailed
		}
	}
	fmt.Fprintf(stdout, "wrote %s and SHA256SUMS: %d templates\n", filepath.Join(outdir, "catalog.json"), count)
	return exitOK
}

// checkSource reads the source layout in dir and checks every template,
// printing one line each. It returns the bundle, the number of templates and
// whether every one passed.
func checkSource(dir, version string, stdout, stderr io.Writer) ([]byte, int, bool) {
	bundle, err := templates.ReadSource(os.DirFS(dir), version)
	if err != nil {
		fmt.Fprintf(stderr, "caravana template: %v\n", err)
		return nil, 0, false
	}
	cat, err := templates.ParseCatalog(bundle)
	if err != nil {
		// ReadSource has parsed it already; this is not expected.
		fmt.Fprintf(stderr, "caravana template: %v\n", err)
		return nil, 0, false
	}
	allOK := true
	for _, t := range cat.Templates {
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
		fmt.Fprintf(stdout, "ok   %s %s\n", t.Name, t.Version)
	}
	return bundle, len(cat.Templates), allOK
}
