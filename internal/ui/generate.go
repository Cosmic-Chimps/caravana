// Package ui serves Caravana's web pages: sign-in, the console (apps, an app's
// tabs, New app) and Settings.
//
// The stylesheet static/app.css is generated from style/ and committed, so
// go build never needs the style tool. To regenerate it after changing the
// styles or the templates, run go generate ./internal/ui (or
// tools/ui-css.sh build); CI runs tools/ui-css.sh check.
package ui

//go:generate ../../tools/ui-css.sh build
