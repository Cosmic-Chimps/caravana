package service

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// The notes of a real release: 0.15.0's CHANGELOG section, as GitHub would carry it in the release body.
func TestParseReleaseNotesRealSection(t *testing.T) {
	data, err := os.ReadFile("testdata/changelog-0.15.0.md")
	if err != nil {
		t.Fatal(err)
	}
	want := []ReleaseNote{
		{Lead: "API 0.28.0", Colon: true, Text: "`Deployment.registeredAt` (the time Nomad accepted the job, `null` until it did), the time the console splits a deploy's stages on."},
		{Lead: "Configure of an app deployed from a Compose file", Text: "says why it looks different: Caravana keeps the file, so the app is edited through it, while an app deployed from an image is edited field by field."},
		{Lead: "Secrets in the Compose form", Text: "(New app, Deploy from Compose, Configure) are one row each: the name at the left, its input at the right, so a long name such as `Frunchy__Auth__Providers__Umbral__ClientSecret` no longer runs into the next one."},
		{Lead: "MCP", Colon: true, Text: "`deploy_image`, `promote_app` and `redeploy_version` are marked destructive, so agent clients ask before running them."},
		{Text: "An upgrade that rolls back restores Caravana's binary and data only."},
		{Lead: "The installer validates and quotes its options.", Text: "`--email` and `--acme-email` must be plain addresses and `--domain` a host name, or it refuses, naming the option, before changing anything."},
	}
	got := ParseReleaseNotes(string(data), false)
	if !slices.Equal(got, want) {
		t.Errorf("notes =\n%v\nwant\n%v", got, want)
	}

	// A body cut in the middle of a bullet gives only the bullets that were whole: the cut one is dropped.
	body := string(data)
	at := strings.Index(body, "- An exported jobspec")
	cutBody := body[:at+30] // "- An exported jobspec says in i"
	if got := ParseReleaseNotes(cutBody, true); !slices.Equal(got, want) {
		t.Errorf("a cut body gives %v, want the six whole bullets", got)
	}
	// Cut so that fewer than six are whole: the partial one is not among them.
	short := body[:strings.Index(body, "- **MCP**")+12] // "- **MCP**: `d"
	if got := ParseReleaseNotes(short, true); !slices.Equal(got, want[:3]) {
		t.Errorf("a body cut in the fourth bullet gives %v, want the first three", got)
	}
	// The same text, not cut, keeps its last bullet whole as far as it goes.
	if got := ParseReleaseNotes(short, false); len(got) != 4 {
		t.Errorf("an uncut body gives %d notes, want 4", len(got))
	}
}

func TestParseReleaseNotes(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		cut  bool
		want []ReleaseNote
	}{
		{"no bullets", "no bullets here", false, nil},
		{"empty", "", false, nil},
		{"bold lead and the first sentence of the rest", "- **API 0.28.0**: `Deployment.registeredAt` (the time it was accepted), more. Another.", false,
			[]ReleaseNote{{Lead: "API 0.28.0", Colon: true, Text: "`Deployment.registeredAt` (the time it was accepted), more."}}},
		{"a lead the text goes on from takes no colon", "- **Configure** says why: it is new. More.", false,
			[]ReleaseNote{{Lead: "Configure", Text: "says why: it is new."}}},
		{"a lead that ends in a stop takes no colon", "- **It validates.**: and quotes. More.", false,
			[]ReleaseNote{{Lead: "It validates.", Text: "and quotes."}}},
		{"a lead alone", "- **Only a lead**", false, []ReleaseNote{{Lead: "Only a lead"}}},
		{"a colon with nothing after it", "- **Lead**:", false, []ReleaseNote{{Lead: "Lead"}}},
		{"other bold in the rest is dropped", "- **Lead**: uses **bold** here. More.", false,
			[]ReleaseNote{{Lead: "Lead", Colon: true, Text: "uses bold here."}}},
		{"first sentence, never cut mid-sentence", "- Configure says why it looks different. And a second sentence.", false,
			[]ReleaseNote{{Text: "Configure says why it looks different."}}},
		{"a long sentence stays whole", "- " + strings.Repeat("word ", 80) + "end. Next.", false,
			[]ReleaseNote{{Text: strings.Repeat("word ", 80) + "end."}}},
		{"wrapped lines are joined", "- First line of a bullet\n  that goes on\n  and ends here. Second.\n- Next.", false,
			[]ReleaseNote{{Text: "First line of a bullet that goes on and ends here."}, {Text: "Next."}}},
		{"a lead that wraps", "- **A lead that\n  wraps** and the rest.", false,
			[]ReleaseNote{{Lead: "A lead that wraps", Text: "and the rest."}}},
		{"a dot inside backticks or a version is not a sentence end", "- Run `a. b` on 0.15.0 to see it. Then more.", false,
			[]ReleaseNote{{Text: "Run `a. b` on 0.15.0 to see it."}}},
		{"an abbreviation is not a sentence end", "- Set it up, e.g. with a flag, then go. More.", false,
			[]ReleaseNote{{Text: "Set it up, e.g. with a flag, then go."}}},
		{"no full stop at all", "- A bullet with no stop", false, []ReleaseNote{{Text: "A bullet with no stop"}}},
		{"other bold is dropped, other Markdown is left", "- Uses **bold** and _emphasis_ and [a link](https://x.test).", false,
			[]ReleaseNote{{Text: "Uses bold and _emphasis_ and [a link](https://x.test)."}}},
		{"stars and dashes are both bullets, a nested one is not read", "* one.\n  - nested\n- two.\n-not a bullet", false,
			[]ReleaseNote{{Text: "one."}, {Text: "two."}}},
		{"headings and blank lines end a bullet", "### Added\n- one\n\nlazy text\n## Fixed\n- two", false,
			[]ReleaseNote{{Text: "one"}, {Text: "two"}}},
		{"an empty bold lead falls back to the sentence", "- **** words here.", false, []ReleaseNote{{Text: "words here."}}},
		{"an unclosed bold is text", "- **Not closed. Next sentence.", false, []ReleaseNote{{Text: "Not closed."}}},
		{"at most six", "- 1\n- 2\n- 3\n- 4\n- 5\n- 6\n- 7\n- 8", false,
			[]ReleaseNote{{Text: "1"}, {Text: "2"}, {Text: "3"}, {Text: "4"}, {Text: "5"}, {Text: "6"}}},
		{"a cut body drops its open bullet", "- one.\n- two. and the sec", true, []ReleaseNote{{Text: "one."}}},
		{"a cut body that ends after a blank line keeps its last bullet", "- one.\n- two.\n\n", true,
			[]ReleaseNote{{Text: "one."}, {Text: "two."}}},
		{"a cut body that ends at a heading keeps its last bullet", "- one.\n- two.\n## Fixed", true,
			[]ReleaseNote{{Text: "one."}, {Text: "two."}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseReleaseNotes(tc.body, tc.cut)
			if !slices.Equal(got, tc.want) {
				t.Errorf("notes = %v, want %v", got, tc.want)
			}
		})
	}
}

// The security updates' wait: over 24 hours after the count first went above zero, never when the
// clock isn't known.
func TestSecurityGraceOver(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		since time.Time
		want  bool
	}{
		{"no clock", time.Time{}, false},
		{"an hour in", now.Add(-time.Hour), false},
		{"a minute short of a day", now.Add(-24*time.Hour + time.Minute), false},
		{"exactly a day", now.Add(-24 * time.Hour), true},
		{"two days", now.Add(-48 * time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (UpdateCounts{SecuritySince: tc.since}).SecurityGraceOver(now); got != tc.want {
				t.Errorf("SecurityGraceOver = %v, want %v", got, tc.want)
			}
		})
	}
}

// A note is one line for the API: the lead, a colon or a space, then the sentence.
func TestReleaseNoteString(t *testing.T) {
	for _, tc := range []struct {
		note ReleaseNote
		want string
	}{
		{ReleaseNote{Text: "Only a sentence."}, "Only a sentence."},
		{ReleaseNote{Lead: "MCP", Colon: true, Text: "`deploy_image` is destructive."}, "MCP: `deploy_image` is destructive."},
		{ReleaseNote{Lead: "Configure", Text: "says why."}, "Configure says why."},
		{ReleaseNote{Lead: "Only a lead"}, "Only a lead"},
	} {
		if got := tc.note.String(); got != tc.want {
			t.Errorf("%+v = %q, want %q", tc.note, got, tc.want)
		}
	}
}

// The service keeps at most 16 KiB of a release's notes, and tells the parser when that cut something.
func TestNotesCut(t *testing.T) {
	if notesCut(strings.Repeat("a", releaseBodyLimit-4)) {
		t.Error("a body well under the limit is not cut")
	}
	if !notesCut(strings.Repeat("a", releaseBodyLimit)) || !notesCut(strings.Repeat("a", releaseBodyLimit-2)) {
		t.Error("a body at the limit, or a character short of it, was cut")
	}
}
