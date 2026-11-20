// Package vulns reads the result of an image scan (feature 044). The scanner
// (Grype) writes its findings with a template of ours, Template, in a line
// format that is easy to parse and says plainly when the scan didn't finish;
// Parse turns that text into Findings.
//
// The package is pure: it does no I/O, so the same text always gives the same
// Result, and the Nomad job that runs the scanner (package nomadjob) can use
// Template without any dependency on how the job is run.
package vulns

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Limits on what Caravana keeps of one scan. They bound the memory a parse
// takes and the rows stored per image, whatever a hostile image makes the
// scanner print.
const (
	// MaxFindings is the most findings one Result holds. When a scan has more,
	// the least severe ones are dropped and Result.Cut is set.
	MaxFindings = 20000
	// MaxResultBytes is the most text Parse reads.
	MaxResultBytes = 4 << 20
)

// Severity is how serious an advisory is, on one scale of five: Grype's six
// levels with its "Negligible" and "Unknown" both read as Info, so a reader of
// the findings (the console, another tool) has one scale to learn. The order is
// the order of seriousness, so Critical > High compares as it reads.
type Severity int

// The severities, least serious first. Info is the zero value.
const (
	Info Severity = iota
	Low
	Medium
	High
	Critical
)

// severityNames are the lowercase names, indexed by Severity.
var severityNames = [...]string{"info", "low", "medium", "high", "critical"}

// String is the severity's lowercase name: "critical", "high", ...
func (s Severity) String() string {
	if s < 0 || int(s) >= len(severityNames) {
		return severityNames[Info]
	}
	return severityNames[s]
}

// FromGrype reads a severity as Grype prints it ("Critical", "High", ...),
// ignoring case. "Negligible", "Unknown" and anything else are Info.
func FromGrype(s string) Severity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return Critical
	case "high":
		return High
	case "medium":
		return Medium
	case "low":
		return Low
	}
	return Info
}

// FixStateFixed is the fix state of an advisory the scanner knows a fixed
// version for. The other states ("not-fixed", "wont-fix", "unknown", or
// empty) have no fix to offer.
const (
	FixStateFixed   = "fixed"
	FixStateWontFix = "wont-fix"
)

// EPSS is the Exploit Prediction Scoring System's estimate for an advisory:
// the probability, from 0 to 1, that it is exploited in the next 30 days, and
// where that puts it among all advisories (also 0 to 1).
type EPSS struct {
	Score, Percentile float64
}

// Finding is one advisory that hits one package of one image.
type Finding struct {
	// ID is the finding's stable identity: the first 16 hex digits of the
	// SHA-256 of "grype|advisory|package|version|type|image". The same check
	// on the same asset has the same ID on every scan, so a reader can follow
	// a finding across scans.
	ID string
	// Advisory is the scanner's ID for it: CVE-..., GHSA-..., ALPINE-...
	Advisory string
	// CVE is the advisory when it is a CVE, else the first CVE among its
	// related advisories, else "".
	CVE string
	// Package, Version and Type say what is affected: "zlib", "1.2.11-r3",
	// "apk".
	Package, Version, Type string
	// FixState is the scanner's word for the fix: "fixed", "not-fixed",
	// "wont-fix", "unknown", or empty.
	FixState string
	// FixedIn lists the versions that fix it (empty unless FixState is
	// "fixed").
	FixedIn []string
	// URL is the advisory's page. Only an https link is kept, so a page that
	// shows it as a link can't be made to point elsewhere.
	URL      string
	Severity Severity
	// EPSS is nil when the scanner gave none.
	EPSS *EPSS
	// KEV says the advisory is in CISA's Known Exploited Vulnerabilities
	// catalog: it is being exploited now.
	KEV bool
}

// Fixable says a fixed version exists: the owner can act on it.
func (f Finding) Fixable() bool { return f.FixState == FixStateFixed && len(f.FixedIn) > 0 }

// Remediation says what to do about the finding, in a sentence.
func (f Finding) Remediation() string {
	switch {
	case f.Fixable() && len(f.FixedIn) == 1:
		return fmt.Sprintf("Upgrade %s to %s or later and rebuild the image.", f.Package, f.FixedIn[0])
	case f.Fixable():
		return fmt.Sprintf("Upgrade %s to %s (the one for your release) or later and rebuild the image.", f.Package, strings.Join(f.FixedIn, " or "))
	case f.FixState == FixStateWontFix:
		return "The distribution won't fix it."
	}
	return "No fix is known yet."
}

// Result is one scan's findings.
type Result struct {
	// Findings are sorted: the most severe first, then the fixable ones, then
	// the exploited ones, then by package, advisory and version.
	Findings []Finding
	// DBBuilt is when the vulnerability database the scan used was built. The
	// zero time means the scanner didn't say.
	DBBuilt time.Time
	// DBSchema is the database's schema version, as the scanner says it.
	DBSchema string
	// Scanner is the scanner's version, as it says it ("0.117.0").
	Scanner string
	// Cut says the scan had more than MaxFindings: the least severe were
	// dropped, so Counts is a lower bound.
	Cut bool
}

// Counts is how many findings of each severity a Result has, how many of the
// two worst severities have a fix, and how many are known to be exploited.
type Counts struct {
	Critical, High, Medium, Low, Info int
	FixableCritical, FixableHigh      int
	KEV                               int
}

// Counts counts the findings by severity.
func (r Result) Counts() Counts {
	var c Counts
	for _, f := range r.Findings {
		switch f.Severity {
		case Critical:
			c.Critical++
			if f.Fixable() {
				c.FixableCritical++
			}
		case High:
			c.High++
			if f.Fixable() {
				c.FixableHigh++
			}
		case Medium:
			c.Medium++
		case Low:
			c.Low++
		default:
			c.Info++
		}
		if f.KEV {
			c.KEV++
		}
	}
	return c
}

// Total is the number of findings counted.
func (c Counts) Total() int { return c.Critical + c.High + c.Medium + c.Low + c.Info }

// header and the line tags of the text Template makes, and how many tab-
// separated fields each line has (the tag included).
const (
	header       = "caravana-scan v1"
	matchTag     = "m"
	trailerTag   = "db"
	matchFields  = 13
	trailerCount = 4
)

// Template is the Grype output template (Go text/template with the sprig
// functions) that makes the text Parse reads:
//
//	caravana-scan v1
//	m <TAB> id <TAB> severity <TAB> fix state <TAB> fixed versions (joined by ",")
//	  <TAB> package <TAB> version <TAB> type <TAB> advisory link
//	  <TAB> related advisory IDs (joined by ",") <TAB> EPSS score <TAB> EPSS percentile
//	  <TAB> "kev" or nothing
//	db <TAB> database build time <TAB> database schema <TAB> Grype's version
//
// The header and the trailer are what tell a finished scan from one that was
// cut off. Every field has its tabs, newlines and carriage returns replaced
// with spaces (the "f" helper), so a package name can't forge a line or a
// field: only the template's own tabs separate fields. Nothing else of Grype's
// document is printed: no description, no image name, nothing the image wrote
// but package names and versions.
const Template = `caravana-scan v1` +
	`{{- define "f" }}{{ printf "%s" . | replace "\t" " " | replace "\n" " " | replace "\r" " " }}{{ end }}` +
	`{{- range .Matches }}` + "\n" +
	`m` +
	"\t" + `{{ template "f" .Vulnerability.ID }}` +
	"\t" + `{{ template "f" .Vulnerability.Severity }}` +
	"\t" + `{{ template "f" .Vulnerability.Fix.State }}` +
	"\t" + `{{ template "f" (join "," .Vulnerability.Fix.Versions) }}` +
	"\t" + `{{ template "f" .Artifact.Name }}` +
	"\t" + `{{ template "f" .Artifact.Version }}` +
	"\t" + `{{ template "f" .Artifact.Type }}` +
	"\t" + `{{ template "f" .Vulnerability.DataSource }}` +
	"\t" + `{{ range $i, $r := .RelatedVulnerabilities }}{{ if $i }},{{ end }}{{ template "f" $r.ID }}{{ end }}` +
	// EPSS: the first entry's two numbers (they can't hold a tab: they are floats).
	"\t" + `{{ with .Vulnerability.EPSS }}{{ with index . 0 }}{{ .EPSS }}{{ end }}{{ end }}` +
	"\t" + `{{ with .Vulnerability.EPSS }}{{ with index . 0 }}{{ .Percentile }}{{ end }}{{ end }}` +
	"\t" + `{{ if .Vulnerability.KnownExploited }}kev{{ end }}` +
	`{{- end }}` + "\n" +
	`db` +
	"\t" + `{{ template "f" .Descriptor.DB.Status.Built }}` +
	"\t" + `{{ template "f" .Descriptor.DB.Status.SchemaVersion }}` +
	"\t" + `{{ template "f" .Descriptor.Version }}` + "\n"

// Parse reads the text Template makes for image (the digest that was scanned,
// repo@sha256:..., which only goes into the findings' IDs). A text that has no
// header, no trailer, a line that isn't a match line or text after the
// trailer is an error: the scan didn't finish, or the scanner is not the one
// Caravana expects. Errors name a line number and never quote the text, which
// is the image's.
//
// Findings with the same advisory, package and version are merged into one:
// the worse severity, every fixed version, "fixed" if any says so, exploited
// if any says so. Control characters in a field become spaces, and a link that
// isn't https is dropped.
func Parse(image, text string) (Result, error) {
	if len(text) > MaxResultBytes {
		return Result{}, fmt.Errorf("the scan result is longer than %d bytes", MaxResultBytes)
	}
	lines := strings.Split(text, "\n")
	// A trailing newline leaves one empty last element.
	for len(lines) > 0 && strings.TrimRight(lines[len(lines)-1], "\r") == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != header {
		return Result{}, fmt.Errorf("the scan result has no %q header", header)
	}

	var res Result
	type key struct{ advisory, pkg, version string }
	index := map[key]int{}
	sawTrailer := false
	for i, line := range lines[1:] {
		n := i + 2 // line numbers start at 1; the header was line 1
		line = strings.TrimRight(line, "\r")
		if sawTrailer {
			return Result{}, fmt.Errorf("the scan result has text after its trailer (line %d)", n)
		}
		fields := strings.Split(line, "\t")
		switch fields[0] {
		case matchTag:
			if len(fields) != matchFields {
				return Result{}, fmt.Errorf("line %d of the scan result is not a finding (%d fields, want %d)", n, len(fields), matchFields)
			}
			f, err := finding(fields[1:])
			if err != nil {
				return Result{}, fmt.Errorf("line %d of the scan result: %w", n, err)
			}
			k := key{f.Advisory, f.Package, f.Version}
			if at, ok := index[k]; ok {
				res.Findings[at] = merge(res.Findings[at], f)
				continue
			}
			index[k] = len(res.Findings)
			res.Findings = append(res.Findings, f)
		case trailerTag:
			if len(fields) != trailerCount {
				return Result{}, fmt.Errorf("line %d of the scan result is not a trailer (%d fields, want %d)", n, len(fields), trailerCount)
			}
			// A build time the scanner didn't give, or in a form we don't
			// know, is "unknown", not a failed scan: the findings are good.
			res.DBBuilt = buildTime(fields[1])
			res.DBSchema = clean(fields[2])
			res.Scanner = clean(fields[3])
			sawTrailer = true
		default:
			return Result{}, fmt.Errorf("line %d of the scan result is not a finding or a trailer", n)
		}
	}
	if !sawTrailer {
		return Result{}, fmt.Errorf("the scan result has no %q trailer: the scan did not finish", trailerTag)
	}

	for i := range res.Findings {
		res.Findings[i].ID = findingID(image, res.Findings[i])
	}
	slices.SortFunc(res.Findings, compare)
	if len(res.Findings) > MaxFindings {
		res.Findings = res.Findings[:MaxFindings]
		res.Cut = true
	}
	return res, nil
}

// findingID is a finding's stable ID (see Finding.ID).
func findingID(image string, f Finding) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"grype", f.Advisory, f.Package, f.Version, f.Type, image}, "|")))
	return hex.EncodeToString(sum[:])[:16]
}

// buildTimeLayouts are the forms the template's build time comes in. The
// template prints Grype's time value as Go does (lab, Grype v0.117.0:
// "2026-11-19 06:23:17 +0000 UTC"); RFC 3339 is what a template that formats
// it gives.
var buildTimeLayouts = []string{time.RFC3339, "2006-01-02 15:04:05 -0700 MST"}

// buildTime reads the trailer's build time; the zero time when it can't.
func buildTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range buildTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// finding builds one Finding (without its ID) from the twelve fields after the
// "m" tag.
func finding(f []string) (Finding, error) {
	out := Finding{
		Advisory: clean(f[0]),
		Severity: FromGrype(f[1]),
		FixState: strings.ToLower(clean(f[2])),
		Package:  clean(f[4]),
		Version:  clean(f[5]),
		Type:     clean(f[6]),
		KEV:      clean(f[11]) == "kev",
	}
	if out.Advisory == "" || out.Package == "" {
		return Finding{}, errors.New("a finding has no advisory or no package")
	}
	for _, v := range strings.Split(f[3], ",") {
		if v = clean(v); v != "" {
			out.FixedIn = append(out.FixedIn, v)
		}
	}
	if url := clean(f[7]); strings.HasPrefix(url, "https://") && !strings.ContainsAny(url, " ") {
		out.URL = url
	}
	out.CVE = cveOf(out.Advisory, f[8])
	// A score that isn't a number in [0, 1] is no score; an advisory with a
	// score but no percentile has none either.
	if score, err := strconv.ParseFloat(clean(f[9]), 64); err == nil {
		if pct, err := strconv.ParseFloat(clean(f[10]), 64); err == nil &&
			score >= 0 && score <= 1 && pct >= 0 && pct <= 1 {
			out.EPSS = &EPSS{Score: score, Percentile: pct}
		}
	}
	return out, nil
}

// cveOf is the advisory when it is a CVE, else the first CVE among the related
// IDs (a comma-separated list), else "".
func cveOf(advisory, related string) string {
	if isCVE(advisory) {
		return advisory
	}
	for _, r := range strings.Split(related, ",") {
		if r = clean(r); isCVE(r) {
			return r
		}
	}
	return ""
}

func isCVE(id string) bool { return len(id) > 4 && strings.EqualFold(id[:4], "CVE-") }

// clean turns control characters into spaces and trims the result: what the
// scanner prints comes from the image, and a page or a terminal that shows it
// must not be driven by it.
func clean(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s))
}

// merge combines two findings of the same advisory, package and version: the
// worse severity, every fixed version, the fixed state when either has one, the
// first link, CVE and EPSS there is, exploited when either is.
func merge(a, b Finding) Finding {
	if b.Severity > a.Severity {
		a.Severity = b.Severity
	}
	if b.FixState == FixStateFixed {
		a.FixState = FixStateFixed
	}
	a.FixedIn = append(a.FixedIn, b.FixedIn...)
	slices.Sort(a.FixedIn)
	a.FixedIn = slices.Compact(a.FixedIn)
	if a.URL == "" {
		a.URL = b.URL
	}
	if a.CVE == "" {
		a.CVE = b.CVE
	}
	if a.EPSS == nil {
		a.EPSS = b.EPSS
	}
	a.KEV = a.KEV || b.KEV
	return a
}

// compare sorts findings: severity descending, then fixable, then exploited
// ones first, then by package, advisory and version, so the order never
// depends on the scanner's.
func compare(a, b Finding) int {
	if c := cmp.Compare(b.Severity, a.Severity); c != 0 {
		return c
	}
	if a.Fixable() != b.Fixable() {
		if a.Fixable() {
			return -1
		}
		return 1
	}
	if a.KEV != b.KEV {
		if a.KEV {
			return -1
		}
		return 1
	}
	return cmp.Or(
		cmp.Compare(a.Package, b.Package),
		cmp.Compare(a.Advisory, b.Advisory),
		cmp.Compare(a.Version, b.Version),
	)
}
