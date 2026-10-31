package service

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The notes of a real release: 0.15.0's CHANGELOG section, as GitHub would carry it in the release body.
func TestParseReleaseNotesRealSection(t *testing.T) {
	data, err := os.ReadFile("testdata/changelog-0.15.0.md")
	if err != nil {
		t.Fatal(err)
	}
	want := []ReleaseNote{
		{Text: "API 0.28.0", Lead: true},
		{Text: "Configure of an app deployed from a Compose file", Lead: true},
		{Text: "Secrets in the Compose form", Lead: true},
		{Text: "MCP", Lead: true},
		{Text: "An upgrade that rolls back restores Caravana's binary and data only."},
		{Text: "The installer validates and quotes its options.", Lead: true},
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
		{"bold lead", "- **API 0.28.0**: `Deployment.registeredAt` (the time it was accepted), more.", false,
			[]ReleaseNote{{Text: "API 0.28.0", Lead: true}}},
		{"first sentence, never cut mid-sentence", "- Configure says why it looks different. And a second sentence.", false,
			[]ReleaseNote{{Text: "Configure says why it looks different."}}},
		{"a long sentence stays whole", "- " + strings.Repeat("word ", 80) + "end. Next.", false,
			[]ReleaseNote{{Text: strings.Repeat("word ", 80) + "end."}}},
		{"wrapped lines are joined", "- First line of a bullet\n  that goes on\n  and ends here. Second.\n- Next.", false,
			[]ReleaseNote{{Text: "First line of a bullet that goes on and ends here."}, {Text: "Next."}}},
		{"a lead that wraps", "- **A lead that\n  wraps** and the rest.", false,
			[]ReleaseNote{{Text: "A lead that wraps", Lead: true}}},
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

// The service keeps at most 16 KiB of a release's notes, and tells the parser when that cut something.
func TestNotesCut(t *testing.T) {
	if notesCut(strings.Repeat("a", releaseBodyLimit-4)) {
		t.Error("a body well under the limit is not cut")
	}
	if !notesCut(strings.Repeat("a", releaseBodyLimit)) || !notesCut(strings.Repeat("a", releaseBodyLimit-2)) {
		t.Error("a body at the limit, or a character short of it, was cut")
	}
}
