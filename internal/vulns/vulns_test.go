package vulns

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"
)

const testImage = "traefik/whoami@sha256:1699d99cb4b9ba7d1d9a54ad9a3a9ed8e1a4ee0a0bb1d1d1c1e5a1b1d5e0f4d"

// line makes a match line from its twelve fields.
func line(f ...string) string {
	if len(f) != matchFields-1 {
		panic("a match line has twelve fields")
	}
	return "m\t" + strings.Join(f, "\t") + "\n"
}

// trailer makes the db line.
const trailer = "db\t2026-11-19 06:23:17 +0000 UTC\tv6.1.10\t0.117.0\n"

// sample is what the template makes for alpine:3.14.0 (research R2), with an
// EPSS score and a KEV flag added to the first line.
func sample() string {
	return "caravana-scan v1\n" +
		line("CVE-2022-37434", "Critical", "fixed", "1.2.12-r2", "zlib", "1.2.11-r3", "apk", "https://security.alpinelinux.org/vuln/CVE-2022-37434", "", "0.0123", "0.78", "kev") +
		line("CVE-2024-5535", "Critical", "unknown", "", "libssl1.1", "1.1.1k-r0", "apk", "https://nvd.nist.gov/vuln/detail/CVE-2024-5535", "", "", "", "") +
		line("CVE-2022-48174", "Critical", "", "", "busybox", "1.33.1-r2", "apk", "https://nvd.nist.gov/vuln/detail/CVE-2022-48174", "", "", "", "") +
		trailer
}

func TestParseSample(t *testing.T) {
	r, err := Parse(testImage, sample())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 3 || r.Cut {
		t.Fatalf("findings = %d, cut = %v", len(r.Findings), r.Cut)
	}
	if want := time.Date(2026, 11, 19, 6, 23, 17, 0, time.UTC); !r.DBBuilt.Equal(want) {
		t.Errorf("DBBuilt = %v, want %v", r.DBBuilt, want)
	}
	if r.DBSchema != "v6.1.10" || r.Scanner != "0.117.0" {
		t.Errorf("DBSchema = %q, Scanner = %q", r.DBSchema, r.Scanner)
	}
	// Sorted: the fixable critical first, then by package.
	first := r.Findings[0]
	if first.Advisory != "CVE-2022-37434" || !first.Fixable() || first.Package != "zlib" || first.Version != "1.2.11-r3" ||
		first.Type != "apk" || first.Severity != Critical || first.FixState != "fixed" ||
		len(first.FixedIn) != 1 || first.FixedIn[0] != "1.2.12-r2" ||
		first.URL != "https://security.alpinelinux.org/vuln/CVE-2022-37434" || first.CVE != "CVE-2022-37434" || !first.KEV ||
		first.EPSS == nil || first.EPSS.Score != 0.0123 || first.EPSS.Percentile != 0.78 {
		t.Errorf("first = %+v", first)
	}
	if r.Findings[1].Package != "busybox" || r.Findings[2].Package != "libssl1.1" {
		t.Errorf("order = %s, %s; want busybox then libssl1.1", r.Findings[1].Package, r.Findings[2].Package)
	}
	if r.Findings[1].Fixable() || r.Findings[2].Fixable() || r.Findings[1].KEV || r.Findings[1].EPSS != nil {
		t.Error("a finding with an unknown or empty fix state is not fixable, and has no EPSS or KEV of its own")
	}
	if c := r.Counts(); c.Critical != 3 || c.FixableCritical != 1 || c.High != 0 || c.KEV != 1 || c.Total() != 3 {
		t.Errorf("counts = %+v", c)
	}
}

func TestFindingIDIsStable(t *testing.T) {
	a, _ := Parse(testImage, sample())
	b, _ := Parse(testImage, sample())
	other, _ := Parse("traefik/whoami@sha256:"+strings.Repeat("a", 64), sample())
	if a.Findings[0].ID == "" || len(a.Findings[0].ID) != 16 {
		t.Fatalf("ID = %q, want 16 hex digits", a.Findings[0].ID)
	}
	seen := map[string]bool{}
	for i, f := range a.Findings {
		if f.ID != b.Findings[i].ID {
			t.Errorf("finding %d: the ID changed between two parses", i)
		}
		if f.ID == other.Findings[i].ID {
			t.Errorf("finding %d: the same ID on another image", i)
		}
		if seen[f.ID] {
			t.Errorf("finding %d: ID %s is not unique", i, f.ID)
		}
		seen[f.ID] = true
	}
	// The recipe is documented, so a reader of the findings can recompute it.
	want := sha256.Sum256([]byte("grype|CVE-2022-37434|zlib|1.2.11-r3|apk|" + testImage))
	if got := a.Findings[0].ID; got != hex.EncodeToString(want[:])[:16] {
		t.Errorf("ID = %s, want the first 16 hex digits of the SHA-256 of grype|advisory|package|version|type|image", got)
	}
	// The ID doesn't depend on the line's other fields.
	c, _ := Parse(testImage, strings.Replace(sample(), "fixed\t1.2.12-r2", "fixed\t1.2.13", 1))
	if c.Findings[0].ID != a.Findings[0].ID {
		t.Error("the ID changed when only the fixed version did")
	}
}

func TestParseErrors(t *testing.T) {
	m := line("CVE-1", "High", "fixed", "1.0", "p", "0.9", "apk", "", "", "", "", "")
	tests := []struct {
		name, text, want string
	}{
		{"empty", "", "header"},
		{"no header", trailer, "header"},
		{"wrong header version", "caravana-scan v2\n" + trailer, "header"},
		{"no trailer: the scan was cut off", "caravana-scan v1\n" + m, "trailer"},
		{"a short match line", "caravana-scan v1\nm\tCVE-1\tHigh\n" + trailer, "line 2"},
		{"a match line of the old shape", "caravana-scan v1\nm\tCVE-1\tHigh\tfixed\t1.0\tp\t0.9\tapk\t\n" + trailer, "line 2"},
		{"an unknown line", "caravana-scan v1\nx\tfoo\n" + trailer, "line 2"},
		{"a short trailer", "caravana-scan v1\ndb\t2026\tv6\n", "line 2"},
		{"text after the trailer", "caravana-scan v1\n" + trailer + m, "after its trailer"},
		{"no advisory", "caravana-scan v1\n" + line("", "High", "fixed", "1.0", "p", "0.9", "apk", "", "", "", "", "") + trailer, "no advisory"},
		{"no package", "caravana-scan v1\n" + line("CVE-1", "High", "fixed", "1.0", "", "0.9", "apk", "", "", "", "", "") + trailer, "no advisory or no package"},
		{"too long", "caravana-scan v1\n" + strings.Repeat("x", MaxResultBytes), "longer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(testImage, tc.text)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// An error never quotes the scanner's text: it is the image's.
func TestParseErrorsDoNotQuoteTheText(t *testing.T) {
	_, err := Parse(testImage, "caravana-scan v1\nm\ts3cret-sentinel-044\n"+trailer)
	if err == nil || strings.Contains(err.Error(), "s3cret-sentinel-044") {
		t.Errorf("error = %v", err)
	}
}

func TestParseTrailerAloneAndLineEndings(t *testing.T) {
	r, err := Parse(testImage, "caravana-scan v1\r\ndb\t2026-11-19T06:23:17Z\tv6\t0.117.0\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 0 || r.DBBuilt.IsZero() {
		t.Errorf("result = %+v", r)
	}
}

func TestParseBuildTimeForms(t *testing.T) {
	want := time.Date(2026, 11, 19, 6, 23, 17, 0, time.UTC)
	for _, in := range []string{"2026-11-19T06:23:17Z", "2026-11-19 06:23:17 +0000 UTC", "2026-11-19T08:23:17+02:00"} {
		r, err := Parse(testImage, "caravana-scan v1\ndb\t"+in+"\tv6\t0.117.0\n")
		if err != nil || !r.DBBuilt.Equal(want) {
			t.Errorf("%q: DBBuilt = %v, err = %v; want %v", in, r.DBBuilt, err, want)
		}
	}
}

func TestParseUnknownBuildTimeIsNotAnError(t *testing.T) {
	r, err := Parse(testImage, "caravana-scan v1\ndb\t\tv6\t0.117.0\n")
	if err != nil {
		t.Fatal(err)
	}
	if !r.DBBuilt.IsZero() {
		t.Errorf("DBBuilt = %v, want the zero time", r.DBBuilt)
	}
}

func TestParseCleansFields(t *testing.T) {
	text := "caravana-scan v1\n" +
		// An escape sequence in a package name, a link that isn't https, a
		// fixed list with blanks.
		line("CVE-1", "HIGH", "FIXED", "1.0, ,1.1", "p\x1b[31mq", "0.9", "apk", "http://evil.example/x", "", "", "", "") +
		line("CVE-2", "Low", "", "", "r", "1", "apk", "https://ok.example/a b", "", "", "", "") +
		line("CVE-3", "Low", "", "", "r", "1", "apk", "javascript:alert(1)", "", "", "", "") +
		"db\t\tv6\t0.117.0\n"
	r, err := Parse(testImage, text)
	if err != nil {
		t.Fatal(err)
	}
	byAdvisory := map[string]Finding{}
	for _, f := range r.Findings {
		byAdvisory[f.Advisory] = f
	}
	f := byAdvisory["CVE-1"]
	if f.Package != "p [31mq" || strings.ContainsRune(f.Package, 0x1b) {
		t.Errorf("package = %q: control characters must become spaces", f.Package)
	}
	if f.Severity != High || f.FixState != "fixed" || strings.Join(f.FixedIn, "|") != "1.0|1.1" {
		t.Errorf("finding = %+v", f)
	}
	for _, id := range []string{"CVE-1", "CVE-2", "CVE-3"} {
		if u := byAdvisory[id].URL; u != "" {
			t.Errorf("%s URL = %q, want it dropped (not https, or with a space)", id, u)
		}
	}
}

func TestParseCVEEPSSAndKEV(t *testing.T) {
	text := "caravana-scan v1\n" +
		// A GHSA advisory with its CVE among the related IDs (the first CVE wins).
		line("GHSA-aaaa", "Critical", "fixed", "2.15.0", "log4j-core", "2.14.1", "java-archive", "https://github.com/advisories/GHSA-aaaa", "GHSA-bbbb,CVE-2021-44228,CVE-2021-45046", "0.99999", "1", "kev") +
		// A GHSA with no CVE at all.
		line("GHSA-cccc", "Low", "", "", "p", "1", "npm", "", "GHSA-dddd", "", "", "") +
		// An advisory that is itself a CVE keeps it, whatever the related IDs say.
		line("cve-2024-1", "High", "", "", "q", "1", "apk", "", "CVE-2099-9", "0.5", "0.6", "") +
		// A score that isn't a number, or out of range, or without its percentile, is no score.
		line("CVE-2024-2", "High", "", "", "q2", "1", "apk", "", "", "high", "0.6", "") +
		line("CVE-2024-3", "High", "", "", "q3", "1", "apk", "", "", "1.5", "0.6", "") +
		line("CVE-2024-4", "High", "", "", "q4", "1", "apk", "", "", "0.5", "", "") +
		"db\t\tv6\t0.117.0\n"
	r, err := Parse(testImage, text)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Finding{}
	for _, f := range r.Findings {
		by[f.Advisory] = f
	}
	if f := by["GHSA-aaaa"]; f.CVE != "CVE-2021-44228" || !f.KEV || f.EPSS == nil || f.EPSS.Score != 0.99999 || f.EPSS.Percentile != 1 {
		t.Errorf("GHSA-aaaa = %+v", f)
	}
	if f := by["GHSA-cccc"]; f.CVE != "" || f.KEV || f.EPSS != nil {
		t.Errorf("GHSA-cccc = %+v", f)
	}
	if f := by["cve-2024-1"]; f.CVE != "cve-2024-1" || f.EPSS == nil || f.EPSS.Percentile != 0.6 {
		t.Errorf("cve-2024-1 = %+v", f)
	}
	for _, id := range []string{"CVE-2024-2", "CVE-2024-3", "CVE-2024-4"} {
		if by[id].EPSS != nil {
			t.Errorf("%s: EPSS = %+v, want none", id, by[id].EPSS)
		}
	}
}

func TestParseMergesDuplicates(t *testing.T) {
	text := "caravana-scan v1\n" +
		line("CVE-1", "Medium", "not-fixed", "", "zlib", "1.0", "apk", "", "", "", "", "") +
		line("CVE-1", "High", "fixed", "1.2,1.1", "zlib", "1.0", "apk", "https://a.example/1", "", "0.1", "0.2", "kev") +
		line("CVE-1", "Low", "fixed", "1.1", "zlib", "1.0", "apk", "https://b.example/1", "", "0.9", "0.9", "") +
		// Same advisory in another package or version is another finding.
		line("CVE-1", "High", "fixed", "1.2", "zlib", "0.9", "apk", "", "", "", "", "") +
		line("CVE-1", "High", "fixed", "1.2", "libz", "1.0", "apk", "", "", "", "", "") +
		"db\t\tv6\t0.117.0\n"
	r, err := Parse(testImage, text)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 3 {
		t.Fatalf("findings = %+v, want 3", r.Findings)
	}
	var merged Finding
	for _, f := range r.Findings {
		if f.Package == "zlib" && f.Version == "1.0" {
			merged = f
		}
	}
	if merged.Severity != High || !merged.Fixable() || strings.Join(merged.FixedIn, "|") != "1.1|1.2" || merged.URL != "https://a.example/1" ||
		!merged.KEV || merged.EPSS == nil || merged.EPSS.Score != 0.1 {
		t.Errorf("merged = %+v", merged)
	}
}

func TestParseSorts(t *testing.T) {
	text := "caravana-scan v1\n" +
		line("CVE-5", "Low", "fixed", "2", "b", "1", "apk", "", "", "", "", "") +
		line("CVE-4", "High", "not-fixed", "", "z", "1", "apk", "", "", "", "", "") +
		line("CVE-3", "High", "fixed", "2", "z", "1", "apk", "", "", "", "", "") +
		line("CVE-2", "Critical", "not-fixed", "", "z", "1", "apk", "", "", "", "", "") +
		line("CVE-1", "High", "fixed", "2", "a", "1", "apk", "", "", "", "", "") +
		line("CVE-0", "High", "fixed", "2", "a", "1", "apk", "", "", "", "", "") +
		// Exploited ones come before the others of their severity and fixability.
		line("CVE-6", "High", "not-fixed", "", "zz", "1", "apk", "", "", "", "", "kev") +
		line("CVE-7", "Negligible", "", "", "n", "1", "apk", "", "", "", "", "") +
		"db\t\tv6\t0.117.0\n"
	r, err := Parse(testImage, text)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range r.Findings {
		got = append(got, f.Advisory)
	}
	// Critical; then High, fixable first (package a: CVE-0, CVE-1; then z:
	// CVE-3), then the unfixable High, the exploited one first; then Low, Info.
	if want := "CVE-2 CVE-0 CVE-1 CVE-3 CVE-6 CVE-4 CVE-5 CVE-7"; strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

func TestParseCutsAtMaxFindings(t *testing.T) {
	var b strings.Builder
	b.WriteString("caravana-scan v1\n")
	// One more than the limit: all Low except one Critical at the very end,
	// which sorting must keep.
	for i := range MaxFindings {
		b.WriteString(line("CVE-"+strconv.Itoa(i), "Low", "", "", "p"+strconv.Itoa(i), "1", "apk", "", "", "", "", ""))
	}
	b.WriteString(line("CVE-last", "Critical", "", "", "q", "1", "apk", "", "", "", "", ""))
	b.WriteString("db\t\tv6\t0.117.0\n")
	r, err := Parse(testImage, b.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != MaxFindings || !r.Cut {
		t.Fatalf("findings = %d, cut = %v", len(r.Findings), r.Cut)
	}
	if r.Findings[0].Advisory != "CVE-last" {
		t.Errorf("first = %s: the cut must drop the least severe", r.Findings[0].Advisory)
	}
}

func TestSeverity(t *testing.T) {
	order := []Severity{Info, Low, Medium, High, Critical}
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			t.Errorf("%v must rank above %v", order[i], order[i-1])
		}
	}
	for in, want := range map[string]Severity{"Critical": Critical, "high": High, " MEDIUM ": Medium, "Low": Low,
		"Negligible": Info, "Unknown": Info, "": Info, "scary": Info} {
		if got := FromGrype(in); got != want {
			t.Errorf("FromGrype(%q) = %v, want %v", in, got, want)
		}
	}
	if Critical.String() != "critical" || Info.String() != "info" || Severity(99).String() != "info" || Severity(-1).String() != "info" {
		t.Error("String")
	}
}

func TestCounts(t *testing.T) {
	r := Result{Findings: []Finding{
		{Severity: Critical, FixState: "fixed", FixedIn: []string{"1"}, KEV: true},
		{Severity: Critical},
		{Severity: High, FixState: "fixed", FixedIn: []string{"1"}},
		{Severity: High, FixState: "fixed"}, // fixed but no version: not fixable
		{Severity: Medium, KEV: true}, {Severity: Low}, {Severity: Low}, {Severity: Info}, {Severity: Info},
	}}
	want := Counts{Critical: 2, High: 2, Medium: 1, Low: 2, Info: 2, FixableCritical: 1, FixableHigh: 1, KEV: 2}
	if got := r.Counts(); got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
}

func TestRemediation(t *testing.T) {
	for _, tc := range []struct {
		f    Finding
		want string
	}{
		{Finding{Package: "zlib", FixState: "fixed", FixedIn: []string{"1.2.12-r2"}}, "Upgrade zlib to 1.2.12-r2 or later and rebuild the image."},
		{Finding{Package: "tomcat", FixState: "fixed", FixedIn: []string{"9.0.99", "10.1.35"}}, "Upgrade tomcat to 9.0.99 or 10.1.35 (the one for your release) or later and rebuild the image."},
		{Finding{Package: "p", FixState: "wont-fix"}, "The distribution won't fix it."},
		{Finding{Package: "p", FixState: "not-fixed"}, "No fix is known yet."},
		{Finding{Package: "p", FixState: "fixed"}, "No fix is known yet."},
		{Finding{Package: "p"}, "No fix is known yet."},
	} {
		if got := tc.f.Remediation(); got != tc.want {
			t.Errorf("Remediation(%+v) = %q, want %q", tc.f, got, tc.want)
		}
	}
}

// TestTemplateMakesWhatParseReads runs the template over a fake Grype document
// (the real field names, found in the lab), with the functions Grype gives
// templates, to see it makes what Parse reads and that a tab or newline in a
// field can't forge a line. The real thing, run against Grype, is
// TestRealScans.
func TestTemplateMakesWhatParseReads(t *testing.T) {
	funcs := template.FuncMap{
		"replace": func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },
		"join":    func(sep string, v []string) string { return strings.Join(v, sep) },
	}
	tmpl, err := template.New("t").Funcs(funcs).Parse(Template)
	if err != nil {
		t.Fatal(err)
	}
	type epss struct{ EPSS, Percentile float64 }
	type fix struct {
		State    string
		Versions []string
	}
	type vuln struct {
		ID, Severity, DataSource string
		Fix                      fix
		EPSS                     []epss
		KnownExploited           []string
	}
	type related struct{ ID string }
	type artifact struct{ Name, Version, Type string }
	type match struct {
		Vulnerability          vuln
		RelatedVulnerabilities []related
		Artifact               artifact
	}
	hostile := match{
		Artifact:               artifact{Name: "evil\tpkg\nm\tCVE-FORGED\tCritical\tfixed\t9\tx\t1\tapk\thttps://x", Version: "1\r2", Type: "apk"},
		RelatedVulnerabilities: []related{{ID: "GHSA-x\ty"}, {ID: "CVE-2021-44228"}},
		Vulnerability: vuln{ID: "GHSA-1", Severity: "High", DataSource: "https://example.com/GHSA-1",
			Fix: fix{State: "fixed", Versions: []string{"1.2", "1.3"}}, EPSS: []epss{{0.25, 0.75}, {0.9, 0.9}}, KnownExploited: []string{"cisa"}},
	}
	plain := match{Artifact: artifact{Name: "p", Version: "1", Type: "apk"}, Vulnerability: vuln{ID: "CVE-2", Severity: "Negligible"}}
	data := map[string]any{
		"Matches": []match{hostile, plain},
		"Descriptor": map[string]any{
			"Version": "0.117.0",
			"DB":      map[string]any{"Status": map[string]any{"Built": "2026-11-19T06:23:17Z", "SchemaVersion": "v6.1.10"}},
		},
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	r, err := Parse(testImage, out.String())
	if err != nil {
		t.Fatalf("Parse: %v\n%q", err, out.String())
	}
	if len(r.Findings) != 2 {
		t.Fatalf("findings = %+v: the hostile name forged a line", r.Findings)
	}
	var f Finding
	for _, x := range r.Findings {
		if x.Advisory == "GHSA-1" {
			f = x
		}
	}
	if f.Severity != High || strings.Join(f.FixedIn, ",") != "1.2,1.3" || f.Version != "1 2" || f.CVE != "CVE-2021-44228" ||
		!f.KEV || f.EPSS == nil || f.EPSS.Score != 0.25 || f.EPSS.Percentile != 0.75 {
		t.Errorf("finding = %+v", f)
	}
	if r.DBBuilt.IsZero() || r.DBSchema != "v6.1.10" || r.Scanner != "0.117.0" {
		t.Errorf("trailer: %v %q %q", r.DBBuilt, r.DBSchema, r.Scanner)
	}
}

// TestRealScans parses what Grype printed with this template in the lab
// (v0.117.0): alpine:3.14.0 (testdata/alpine-3.14.0.txt, the whole output) and
// a log4shell image (testdata/log4shell.txt: its exploited findings and the
// first fifteen of the others).
func TestRealScans(t *testing.T) {
	read := func(name string) Result {
		t.Helper()
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Parse(testImage, string(b))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return r
	}

	alpine := read("alpine-3.14.0.txt")
	if len(alpine.Findings) != 138 || alpine.DBBuilt.IsZero() || alpine.Scanner != "0.117.0" || alpine.DBSchema != "v6.1.10" {
		t.Fatalf("alpine: %d findings, built %v, scanner %q, schema %q", len(alpine.Findings), alpine.DBBuilt, alpine.Scanner, alpine.DBSchema)
	}
	var zlib *Finding
	for i, f := range alpine.Findings {
		if f.Advisory == "CVE-2022-37434" && f.Package == "zlib" {
			zlib = &alpine.Findings[i]
		}
	}
	if zlib == nil || !zlib.Fixable() || zlib.Severity != Critical || zlib.CVE != "CVE-2022-37434" || zlib.EPSS == nil || zlib.EPSS.Score <= 0 || zlib.KEV {
		t.Errorf("alpine's zlib 1.2.11-r3 must have the fixable critical CVE-2022-37434 with an EPSS score and no KEV: %+v", zlib)
	}
	if c := alpine.Counts(); c.Critical == 0 || c.Total() != len(alpine.Findings) || c.KEV != 0 {
		t.Errorf("alpine counts = %+v for %d findings", c, len(alpine.Findings))
	}

	log4 := read("log4shell.txt")
	kev := 0
	var log4j *Finding
	for i, f := range log4.Findings {
		if f.KEV {
			kev++
		}
		if f.Advisory == "GHSA-jfh8-c2jp-5v3q" {
			log4j = &log4.Findings[i]
		}
	}
	if kev != 10 || log4.Counts().KEV != 10 {
		t.Errorf("log4shell: %d exploited findings, want 10", kev)
	}
	// The log4shell advisory is a GHSA: its CVE comes from the related IDs.
	if log4j == nil || log4j.CVE != "CVE-2021-44228" || !log4j.KEV || log4j.Package != "log4j-core" || log4j.Type != "java-archive" || !log4j.Fixable() {
		t.Errorf("log4j = %+v", log4j)
	}
	// The exploited ones come first among the criticals.
	if !log4.Findings[0].KEV {
		t.Errorf("first finding = %+v, want an exploited critical", log4.Findings[0])
	}
}
