package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

// App templates (feature 035): a catalog of known apps, each a manifest plus a
// Compose file with placeholders (package templates). Installing one is a
// normal Compose deploy of a new app, through DeployComposeIfResult. The
// catalog is the one built into this binary, replaced by a newer verified one
// fetched from the catalog repository's latest release, the way the release
// check works (updates.go): one request a day, nothing about the server in it,
// an owner's setting that turns it off.
//
// Secrets rule: a generated secret or a secret input exists in one place, the
// request handed to DeployComposeIfResult, which sends it to the secret store.
// It is never stored, logged, put in an error or returned.

// The catalog check's address and limits.
const (
	// DefaultTemplatesURL is where the catalog repository's latest release
	// publishes SHA256SUMS and catalog.json.
	DefaultTemplatesURL = "https://github.com/Cosmic-Chimps/caravana-templates/releases/latest/download/"

	templatesSumsLimit   = 4 << 10 // what is read of SHA256SUMS
	templatesBundleLimit = 4 << 20 // what is read of catalog.json
)

// Where the catalog in use comes from.
const (
	CatalogEmbedded = "embedded"
	CatalogFetched  = "fetched"
)

// The failures of a catalog check, in Caravana's words. They are stored and
// shown; none holds the server's own text.
const (
	failCatalogDown   = "The catalog's server didn't answer"
	failCatalogNone   = "The catalog has no release yet"
	failCatalogBad    = "The catalog couldn't be read"
	failCatalogSum    = "The catalog didn't match its checksum"
	failCatalogFormat = "The catalog is in a newer format than this Caravana reads"
	failCatalogCheck  = "A template in the catalog didn't pass Caravana's checks"
)

// The refusals of the template calls. A caller tests them with errors.Is.
var (
	// ErrTemplateNotFound: no template has that name.
	ErrTemplateNotFound = errors.New("no such template")
	// ErrTemplateUnavailable: the template is marked broken, or needs a newer
	// Caravana. The error is a *TemplateUnavailableError with the reason.
	ErrTemplateUnavailable = errors.New("that template isn't offered")
)

// TemplateUnavailableError is ErrTemplateUnavailable with the reason, in words
// ready to show.
type TemplateUnavailableError struct{ Name, Reason string }

func (e *TemplateUnavailableError) Error() string {
	return "The " + e.Name + " template isn't offered: " + e.Reason
}

// Is makes errors.Is(err, ErrTemplateUnavailable) true.
func (e *TemplateUnavailableError) Is(target error) bool { return target == ErrTemplateUnavailable }

// templateCheckOff is ErrCheckDisabled in the words of this check.
type templateCheckOff struct{}

func (templateCheckOff) Error() string { return "Checking for new templates is turned off." }

// Is makes errors.Is(err, ErrCheckDisabled) true, so a caller that maps the
// release check's refusal maps this one too.
func (templateCheckOff) Is(target error) bool { return target == ErrCheckDisabled }

// templatesState is what the template catalog keeps between calls.
type templatesState struct {
	// checkMu is held for the length of a check, so two never run at once (the
	// daily one and Check now).
	checkMu sync.Mutex

	mu       sync.Mutex // guards the rest
	started  time.Time  // when the newest check started
	checking bool

	// The fetched catalog, parsed, for the sum it was parsed from.
	cacheSum string
	cache    templates.Catalog
}

// TemplateInfo is a template and whether this Caravana offers it.
type TemplateInfo struct {
	templates.Template
	// Available is false for a template marked broken or one that needs a newer
	// Caravana; Unavailable says why, in words ready to show.
	Available   bool
	Unavailable string
}

// TemplateCatalogView is the state of the catalog in use and of its check.
type TemplateCatalogView struct {
	// Version and Source (CatalogEmbedded or CatalogFetched) say which catalog is
	// offered; EmbeddedVersion is the one built into this binary.
	Version         string
	Source          string
	EmbeddedVersion string
	// Checking is the owner's setting; false means no request is made at all.
	Checking bool
	// CheckedAt is the last attempt, good or not; CheckedOKAt the last one that
	// was answered well; FetchedAt when the fetched catalog was kept. Zero for
	// never.
	CheckedAt, CheckedOKAt, FetchedAt time.Time
	// Error is why the last check failed, in Caravana's words; empty when it
	// worked.
	Error string
	// Running says a check is going now.
	Running bool
}

// TemplateList is the catalog and the templates that match a search.
type TemplateList struct {
	Catalog   TemplateCatalogView
	Templates []TemplateInfo
}

// TemplatePreview is what installing a template would make: the app with
// secret names only, the Compose file's warnings, and the rendered file.
type TemplatePreview struct {
	App      app.App
	Warnings []app.Warning
	// Compose is the rendered Compose file. It holds no secret: a template
	// can't put one there.
	Compose string
}

// TemplateInstallOptions are the owner's choices for one install or preview.
type TemplateInstallOptions struct {
	// WithoutBackups removes every backup policy the template has, and the result
	// carries one warning for each (field services.<service>.backup) saying how to
	// turn it on. It is the owner's explicit choice, for an installation that has no
	// backup destination yet; without it the deploy refuses a backup that has
	// nowhere to go, as it does for any app.
	WithoutBackups bool
}

// withoutBackups removes the backup policy of every service of req, in a copy
// (the request's overlay map is made again; nothing is shared with the template),
// and returns a warning for each, in the services' order by name.
func withoutBackups(req compose.Request) (compose.Request, []app.Warning) {
	var warnings []app.Warning
	services := make(map[string]compose.Overlay, len(req.Services))
	for _, name := range sortedKeys(req.Services) {
		ov := req.Services[name]
		if ov.Backup != nil {
			ov.Backup = nil
			warnings = append(warnings, app.Warning{Field: "services." + name + ".backup", Message: "Backups are off for " + name +
				": set a backup destination in Settings › Backup storage, then turn its backup on in the Backups tab."})
		}
		services[name] = ov
	}
	if len(req.Services) > 0 {
		req.Services = services
	}
	return req, warnings
}

// sortedKeys is the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// SetTemplatesURL sets the address the catalog check asks, in place of the
// catalog repository's latest release: tests and the lab give a fake server.
// It is a directory: SHA256SUMS and catalog.json are read from beneath it. Call
// it before Start; empty keeps the default.
func (s *Service) SetTemplatesURL(u string) {
	if u != "" {
		s.templatesURL = strings.TrimRight(u, "/") + "/"
	}
}

// SetTemplateCheckGap sets the time between two catalog checks (a day by
// default, with up to a twenty-fourth of it added at random); a gap shorter
// than a minute also brings the first check forward. Call it before Start; a
// value that isn't positive keeps the default.
func (s *Service) SetTemplateCheckGap(d time.Duration) {
	if d > 0 {
		s.templatesGap = d
	}
}

// SetTemplateRandom sets where generated secrets come from, in place of
// crypto/rand: a test gives a reader that yields a sentinel. Call it before
// Start; nil keeps crypto/rand.
func (s *Service) SetTemplateRandom(r io.Reader) {
	if r != nil {
		s.templateRand = r
	}
}

// SetTemplateChecking stores the owner's setting: whether Caravana asks the
// catalog repository for a newer catalog. Off means no request at all.
func (s *Service) SetTemplateChecking(ctx context.Context, checking bool) error {
	return s.store.SetTemplateChecking(ctx, checking)
}

// catalogInUse is the catalog Caravana offers: the fetched one when it is
// stored, intact and newer than the built-in one, else the built-in one.
func (s *Service) catalogInUse(ctx context.Context) (templates.Catalog, string, store.TemplateCatalogRow, error) {
	row, err := s.store.TemplateCatalog(ctx)
	if err != nil {
		return templates.Catalog{}, "", store.TemplateCatalogRow{}, err
	}
	embedded, _ := templates.Embedded()
	if len(row.Bundle) == 0 || !templates.Newer(row.Version, embedded.Version) {
		return embedded, CatalogEmbedded, row, nil
	}

	st := &s.templates
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.cacheSum == row.BundleSum && st.cacheSum != "" {
		return st.cache, CatalogFetched, row, nil
	}
	// The row was verified when it was written; a bundle that no longer matches
	// its sum or doesn't parse is not trusted.
	cat, err := templates.ParseCatalog(row.Bundle)
	if err != nil || templates.SumOf(row.Bundle) != row.BundleSum {
		s.log.Warn("the stored template catalog is unreadable; using the built-in one")
		return embedded, CatalogEmbedded, row, nil
	}
	st.cacheSum, st.cache = row.BundleSum, cat
	return cat, CatalogFetched, row, nil
}

// unavailable says why this Caravana doesn't offer t; empty when it does. A
// development build counts as new enough.
func (s *Service) unavailable(t templates.Template) string {
	if t.Broken != "" {
		return t.Broken
	}
	if t.Requires != "" && hostos.IsReleaseVersion(s.runningVersion) && hostos.ReleaseNewer(t.Requires, s.runningVersion) {
		return "needs caravana " + t.Requires + " or newer"
	}
	return ""
}

func (s *Service) info(t templates.Template) TemplateInfo {
	reason := s.unavailable(t)
	return TemplateInfo{Template: t, Available: reason == "", Unavailable: reason}
}

func (s *Service) catalogView(cat templates.Catalog, source string, row store.TemplateCatalogRow) TemplateCatalogView {
	embedded, _ := templates.Embedded()
	st := &s.templates
	st.mu.Lock()
	running := st.checking
	st.mu.Unlock()
	return TemplateCatalogView{
		Version: cat.Version, Source: source, EmbeddedVersion: embedded.Version,
		Checking: row.Checking, CheckedAt: row.CheckedAt, CheckedOKAt: row.CheckedOKAt, FetchedAt: row.FetchedAt,
		Error: row.Error, Running: running,
	}
}

// TemplateCatalog is the state of the catalog in use.
func (s *Service) TemplateCatalog(ctx context.Context) (TemplateCatalogView, error) {
	cat, source, row, err := s.catalogInUse(ctx)
	if err != nil {
		return TemplateCatalogView{}, err
	}
	return s.catalogView(cat, source, row), nil
}

// ListTemplates lists the templates of the catalog in use, with the catalog's
// state. q keeps those whose title, description or a tag has it (any case); tag
// keeps those with exactly that tag. Both may be empty. A template this
// Caravana doesn't offer is listed, with the reason: the console leaves it out.
func (s *Service) ListTemplates(ctx context.Context, q, tag string) (TemplateList, error) {
	cat, source, row, err := s.catalogInUse(ctx)
	if err != nil {
		return TemplateList{}, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := TemplateList{Catalog: s.catalogView(cat, source, row), Templates: []TemplateInfo{}}
	for _, t := range cat.Templates {
		if tag != "" && !containsString(t.Tags, tag) {
			continue
		}
		if q != "" && !matchesQuery(t, q) {
			continue
		}
		out.Templates = append(out.Templates, s.info(t))
	}
	return out, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// matchesQuery says the lowercase q is in the template's title, description or
// one of its tags.
func matchesQuery(t templates.Template, q string) bool {
	if strings.Contains(strings.ToLower(t.Title), q) || strings.Contains(strings.ToLower(t.Description), q) {
		return true
	}
	for _, tag := range t.Tags {
		if strings.Contains(tag, q) {
			return true
		}
	}
	return false
}

// Template returns one template of the catalog in use, with whether it is
// offered. An unknown name is ErrTemplateNotFound.
func (s *Service) Template(ctx context.Context, name string) (TemplateInfo, error) {
	cat, _, _, err := s.catalogInUse(ctx)
	if err != nil {
		return TemplateInfo{}, err
	}
	t, ok := cat.Find(name)
	if !ok {
		return TemplateInfo{}, fmt.Errorf("template %q: %w", name, ErrTemplateNotFound)
	}
	return s.info(t), nil
}

// offered is Template for an install or a preview: a template that isn't
// offered is a *TemplateUnavailableError.
func (s *Service) offered(ctx context.Context, name string) (templates.Template, error) {
	info, err := s.Template(ctx, name)
	if err != nil {
		return templates.Template{}, err
	}
	if !info.Available {
		return templates.Template{}, &TemplateUnavailableError{Name: name, Reason: info.Unavailable}
	}
	return info.Template, nil
}

// PreviewTemplate answers what InstallTemplate would deploy: the rendered file
// and the app it becomes, with secret names only, and the file's warnings. It
// generates nothing, saves nothing and asks nobody: no registry, no Nomad, no
// secret store. A secret input may be empty; every other input is checked as in
// an install. The errors are InstallTemplate's.
func (s *Service) PreviewTemplate(ctx context.Context, name, appID string, inputs map[string]string, opts TemplateInstallOptions) (TemplatePreview, error) {
	t, err := s.offered(ctx, name)
	if err != nil {
		return TemplatePreview{}, err
	}
	if err := s.checkPlace(ctx, appID); err != nil {
		return TemplatePreview{}, err
	}
	checked, err := t.CheckPreviewInputs(inputs)
	if err != nil {
		return TemplatePreview{}, err
	}
	req, err := t.Render(t.PreviewValues(checked))
	if err != nil {
		return TemplatePreview{}, err
	}
	var extra []app.Warning
	if opts.WithoutBackups {
		req, extra = withoutBackups(req)
	}
	a, warnings, err := s.PreviewCompose(ctx, appID, req)
	if err != nil {
		return TemplatePreview{}, templateError(t, err)
	}
	return TemplatePreview{App: a, Warnings: append(warnings, extra...), Compose: req.Compose}, nil
}

// InstallTemplate installs a template as a new app: it checks the inputs,
// makes the generated secrets, renders the Compose file and its overlay and
// deploys them as DeployComposeIfResult deploys any Compose file of a new app
// (an ID that is taken is ErrAppExists), then records the template and its
// version on the app.
//
// An input that is wrong is an *app.FieldError on "inputs.<name>"; so is a
// refusal at a domain host that came from an input, a *DomainTakenError
// included. Any other refusal is a defect of the template and keeps its path in
// the file. With opts.WithoutBackups the template's backup policies are left out
// and each is a warning. The result is the deploy's.
func (s *Service) InstallTemplate(ctx context.Context, name, appID string, inputs map[string]string, opts TemplateInstallOptions, trigger app.Trigger) (DeployResult, error) {
	t, err := s.offered(ctx, name)
	if err != nil {
		return DeployResult{}, err
	}
	// The ID and its place first, so a bad one is a field error on "id" and not
	// the Compose loader's complaint about a project name.
	if err := s.checkPlace(ctx, appID); err != nil {
		return DeployResult{}, err
	}
	checked, err := t.CheckInputs(inputs)
	if err != nil {
		return DeployResult{}, err
	}
	generated, err := t.Generate(s.templateRand)
	if err != nil {
		return DeployResult{}, fmt.Errorf("install template %q: %w", name, err)
	}
	values := make(map[string]string, len(checked)+len(generated))
	maps.Copy(values, checked)
	maps.Copy(values, generated)
	req, err := t.Render(values)
	if err != nil {
		return DeployResult{}, err
	}

	var extra []app.Warning
	if opts.WithoutBackups {
		req, extra = withoutBackups(req)
	}
	res, err := s.deployComposeWarned(ctx, appID, req, Condition{IfNew: true}, trigger, extra)
	// A deploy that fails after the app was saved (the image can't be found, the
	// secret store is down) leaves the app and a failed deployment: the app came
	// from the template all the same.
	var deployErr *DeployError
	if err == nil || errors.As(err, &deployErr) {
		if recErr := s.store.SetAppTemplate(ctx, appID, t.Name, t.Version); recErr != nil {
			// Only the provenance is lost; the error holds no input.
			s.log.Warn("record the template of an app", "app", appID, "template", t.Name, "error", recErr)
		} else {
			s.appChanged(appID)
		}
	}
	if err != nil {
		return res, templateError(t, err)
	}
	return res, nil
}

// templateError puts a refusal at a domain host that came from an input where
// the owner typed it: "inputs.domain", not "services.web.domains[0].host" in a
// file the owner never saw (research R5). Any other error is returned as it is.
func templateError(t templates.Template, err error) error {
	paths := t.DomainInputs()
	var taken *DomainTakenError
	if errors.As(err, &taken) {
		if input, ok := paths[taken.Field]; ok {
			return &DomainTakenError{Field: "inputs." + input, Host: taken.Host, App: taken.App}
		}
		return err
	}
	var fe *app.FieldError
	if errors.As(err, &fe) {
		if input, ok := paths[fe.Field]; ok {
			return app.NewFieldError("inputs."+input, "%s", fe.Message)
		}
	}
	return err
}

// ---- the catalog check ----

// CheckTemplates starts a check of the catalog repository now and returns at
// once; the result arrives on the catalog's state. It is refused with
// ErrCheckDisabled while the owner has checking off (and then makes no
// request), and with ErrCheckTooSoon within 10 minutes of the last check.
func (s *Service) CheckTemplates(ctx context.Context) error {
	row, err := s.store.TemplateCatalog(ctx)
	if err != nil {
		return err
	}
	if !row.Checking {
		return templateCheckOff{}
	}
	now := s.clock.Now()
	st := &s.templates
	st.mu.Lock()
	last := row.CheckedAt
	if st.started.After(last) {
		last = st.started
	}
	if st.checking || (!last.IsZero() && now.Sub(last) < checkMinGap) {
		again := last.Add(checkMinGap)
		st.mu.Unlock()
		return &CheckTooSoonError{Again: again}
	}
	st.started, st.checking = now, true
	st.mu.Unlock()

	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.templateCheck(s.background())
	}()
	return nil
}

// runTemplateCheck checks first after a minute (sooner when the gap is shorter)
// and then every gap with up to a twenty-fourth of it added, until ctx ends. A
// pass does nothing while checking is off.
func (s *Service) runTemplateCheck(ctx context.Context, gap time.Duration) {
	wait := min(releaseFirstCheck, gap)
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		st := &s.templates
		st.mu.Lock()
		st.started = s.clock.Now()
		st.mu.Unlock()
		s.templateCheck(ctx)
		wait = gap + rand.N(gap/24+1)
	}
}

// templateCheck is one check, whoever asked for it. The owner's setting is read
// first and decides whether any request is made at all.
func (s *Service) templateCheck(ctx context.Context) {
	st := &s.templates
	st.checkMu.Lock()
	defer st.checkMu.Unlock()
	defer func() {
		st.mu.Lock()
		st.checking = false
		st.mu.Unlock()
	}()
	switch err := s.fetchAndRecordTemplates(ctx); {
	case err == nil, errors.Is(err, ErrCheckDisabled):
	case ctx.Err() != nil:
	default:
		s.log.Warn("check for a new template catalog", "error", err)
	}
}

// fetchAndRecordTemplates makes the requests, works out what they mean and
// writes the row. Every failure of the fetch is a *checkFailure with fixed
// words, which is what the row keeps.
func (s *Service) fetchAndRecordTemplates(ctx context.Context) error {
	row, err := s.store.TemplateCatalog(ctx)
	if err != nil {
		return err
	}
	if !row.Checking {
		return templateCheckOff{}
	}
	now := s.clock.Now().UTC()
	row.CheckedAt = now

	update, ferr := s.fetchCatalog(ctx, row)
	if ferr != nil {
		var f *checkFailure
		if errors.As(ferr, &f) {
			row.Error = f.words
		} else {
			row.Error = failCatalogDown
		}
	} else {
		row.CheckedOKAt, row.Error = now, ""
		switch {
		case update == nil:
		case update.keep:
			row.Bundle, row.BundleSum, row.Version, row.FetchedAt = update.bundle, update.sum, update.version, now
		case len(row.Bundle) == 0:
			// Seen and not kept: remember its sum so tomorrow's check doesn't download it
			// again. With a kept bundle the sum stays that bundle's, which is what the
			// stored row is verified against.
			row.BundleSum = update.sum
		}
	}
	if err := s.store.SaveTemplateCheck(ctx, row); err != nil {
		return err
	}
	if ferr != nil && ctx.Err() == nil {
		return ferr
	}
	return nil
}

// catalogUpdate is what a good fetch found. A nil update means nothing changed.
type catalogUpdate struct {
	sum     string // the newest bundle's sum, kept or not
	keep    bool   // the bundle is newer than what is in use and is to be kept
	bundle  []byte
	version string
}

// fetchCatalog asks for SHA256SUMS and, only when the catalog changed, for
// catalog.json, and verifies it. Every failure is a *checkFailure.
func (s *Service) fetchCatalog(ctx context.Context, row store.TemplateCatalogRow) (*catalogUpdate, error) {
	sums, err := s.fetchTemplateFile(ctx, "SHA256SUMS", templatesSumsLimit)
	if err != nil {
		return nil, err
	}
	sum, err := templates.SumFromSums(sums)
	if err != nil {
		return nil, &checkFailure{failCatalogBad}
	}
	embedded, embeddedBundle := templates.Embedded()
	if sum == row.BundleSum || sum == templates.SumOf(embeddedBundle) {
		return nil, nil // nothing new: the same catalog as the last check, or the built-in one
	}

	bundle, err := s.fetchTemplateFile(ctx, "catalog.json", templatesBundleLimit)
	if err != nil {
		return nil, err
	}
	if err := templates.VerifySums(bundle, sums); err != nil {
		return nil, &checkFailure{failCatalogSum}
	}
	cat, err := templates.ParseCatalog(bundle)
	switch {
	case errors.Is(err, templates.ErrUnknownFormat):
		return nil, &checkFailure{failCatalogFormat}
	case err != nil:
		return nil, &checkFailure{failCatalogBad}
	}
	if err := templates.CheckCatalog(cat, templates.CheckSettings()); err != nil {
		return nil, &checkFailure{failCatalogCheck}
	}

	// Kept only when newer than the built-in catalog and than the one kept.
	keep := templates.Newer(cat.Version, embedded.Version)
	if len(row.Bundle) > 0 && !templates.Newer(cat.Version, row.Version) {
		keep = false
	}
	return &catalogUpdate{sum: sum, keep: keep, bundle: bundle, version: cat.Version}, nil
}

// fetchTemplateFile reads one file of the catalog's release, at most limit
// bytes.
func (s *Service) fetchTemplateFile(ctx context.Context, name string, limit int) ([]byte, error) {
	address, err := url.JoinPath(s.templatesURL, name)
	if err != nil {
		return nil, &checkFailure{failCatalogDown}
	}
	resp, err := s.get(ctx, address, "")
	if err != nil {
		return nil, &checkFailure{failCatalogDown}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, &checkFailure{failGitHubLimit}
	case resp.StatusCode == http.StatusNotFound:
		return nil, &checkFailure{failCatalogNone}
	case resp.StatusCode != http.StatusOK:
		return nil, &checkFailure{failCatalogDown}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, &checkFailure{failCatalogDown}
	}
	if len(data) > limit {
		return nil, &checkFailure{failCatalogBad}
	}
	return data, nil
}
