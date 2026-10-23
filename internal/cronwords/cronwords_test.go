package cronwords

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// shapes pairs each phrase the console writes with the cron line it means.
var shapes = []struct {
	words string // what Words says for cron
	cron  string
}{
	{"Every minute", "* * * * *"},
	{"Every 2 minutes", "*/2 * * * *"},
	{"Every 5 minutes", "*/5 * * * *"},
	{"Every 15 minutes", "*/15 * * * *"},
	{"Every 30 minutes", "*/30 * * * *"},
	{"Every hour", "0 * * * *"},
	{"Every 2 hours", "0 */2 * * *"},
	{"Every 6 hours", "0 */6 * * *"},
	{"Every 12 hours", "0 */12 * * *"},
	{"Every day at 03:00 UTC", "0 3 * * *"},
	{"Every day at 23:45 UTC", "45 23 * * *"},
	{"Weekdays at 09:00 UTC", "0 9 * * 1-5"},
	{"Mondays at 08:30 UTC", "30 8 * * 1"},
	{"Wednesdays at 12:00 UTC", "0 12 * * 3"},
	{"Saturdays at 01:15 UTC", "15 1 * * 6"},
	{"Sundays at 03:00 UTC", "0 3 * * 0"},
	{"First of the month at 06:00 UTC", "0 6 1 * *"},
	{"First of the month at 22:10 UTC", "10 22 1 * *"},
}

func TestWords(t *testing.T) {
	for _, tc := range shapes {
		t.Run(tc.cron, func(t *testing.T) {
			if got := Words(tc.cron); got != tc.words {
				t.Errorf("Words(%q) = %q, want %q", tc.cron, got, tc.words)
			}
		})
	}
}

// TestWordsLastResort covers valid lines the phrases don't cover, and the odd
// spellings of ones they do.
func TestWordsLastResort(t *testing.T) {
	for _, tc := range []struct{ cron, want string }{
		{"0 0,12 * * *", "On `0 0,12 * * *`"},
		{"5 * * * *", "On `5 * * * *`"},
		{"*/7 * * * *", "On `*/7 * * * *`"},
		{"0 */5 * * *", "On `0 */5 * * *`"},
		{"0 3 * * mon-fri", "On `0 3 * * mon-fri`"},
		{"0 3 1 6 *", "On `0 3 1 6 *`"},
		{"0 3 15 * *", "On `0 3 15 * *`"},
		{"0 3 * * 1,3", "On `0 3 * * 1,3`"},
		{"  0   3  *  * 7 ", "Sundays at 03:00 UTC"},
		{"not a cron line", "On `not a cron line`"},
	} {
		if got := Words(tc.cron); got != tc.want {
			t.Errorf("Words(%q) = %q, want %q", tc.cron, got, tc.want)
		}
	}
}

// TestParseRoundTrip: every phrase Words writes, Parse reads back, in any
// case, with its " UTC" or without.
func TestParseRoundTrip(t *testing.T) {
	for _, tc := range shapes {
		for _, text := range []string{
			tc.words,
			strings.ToLower(tc.words),
			strings.ToUpper(tc.words),
			strings.TrimSuffix(tc.words, " UTC"),
		} {
			if strings.HasSuffix(text, " UTC") || strings.HasSuffix(text, " utc") {
				// Parse reads "at HH:MM"; the console adds "UTC" when drawing.
				text = text[:len(text)-4]
			}
			got, err := Parse(text)
			if err != nil {
				t.Errorf("Parse(%q): %v", text, err)
				continue
			}
			if got != tc.cron {
				t.Errorf("Parse(%q) = %q, want %q", text, got, tc.cron)
			}
		}
	}
}

func TestParse(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		// The four preset chips of the New schedule form.
		{"every 15 minutes", "*/15 * * * *"},
		{"every day at 03:00", "0 3 * * *"},
		{"weekdays at 09:00", "0 9 * * 1-5"},
		{"first of the month", "0 6 1 * *"},
		// Spellings.
		{"  Every   15   Minutes ", "*/15 * * * *"},
		{"every 1 minute", "* * * * *"},
		{"every 1 hour", "0 * * * *"},
		{"every day at 3:05", "5 3 * * *"},
		{"monday at 09:00", "0 9 * * 1"},
		{"Fridays at 17:30", "30 17 * * 5"},
		{"first of the month at 4:00", "0 4 1 * *"},
		// A cron line passes through, with its spaces normalised.
		{"0 3 * * *", "0 3 * * *"},
		{"  */10   *  * * *  ", "*/10 * * * *"},
		{"15 14 1 * *", "15 14 1 * *"},
		{"0 22 * * mon-fri", "0 22 * * mon-fri"},
		{"0 0,12 * * 1-5", "0 0,12 * * 1-5"},
	} {
		got, err := Parse(tc.text)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.text, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestParseRefusals(t *testing.T) {
	const sentence = `Couldn't read that. Try "every 2 hours" or paste a cron line.`
	for _, text := range []string{
		"",
		"   ",
		"sometimes",
		"every 7 minutes",  // doesn't divide 60
		"every 0 minutes",  // zero
		"every 60 minutes", // use "every hour"
		"every 5 hours",    // doesn't divide 24
		"every 24 hours",
		"every day at 25:00",
		"every day at 09:61",
		"every day",
		"weekdays at 9",
		"tuesday",
		"first of the month at 99:00",
		"twice a day",
	} {
		_, err := Parse(text)
		if !errors.Is(err, ErrUnreadable) {
			t.Errorf("Parse(%q) error = %v, want ErrUnreadable", text, err)
			continue
		}
		if err.Error() != sentence {
			t.Errorf("Parse(%q) error text = %q", text, err.Error())
		}
	}
}

func TestParseRefusesBadCronLines(t *testing.T) {
	for _, tc := range []struct {
		text string
		want string // a part of the message
	}{
		{"0 0 31 2 *", "never comes"},     // 31 February
		{"0 0 31 4 *", "never comes"},     // 31 April
		{"* * * * * *", "five fields"},    // six fields
		{"*/30 * * * * *", "five fields"}, // seconds first: six fields
		{"0 0 0 * * * *", "five fields"},  // seven fields
		{"61 * * * *", "can't read"},      // minute out of range
		{"0 24 * * *", "can't read"},      // hour out of range
		{"0 0 0 * *", "can't read"},       // day 0
		{"0 0 * 13 *", "can't read"},      // month 13
	} {
		_, err := Parse(tc.text)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) error = %v, want it to hold %q", tc.text, err, tc.want)
		}
	}
	// Not in cron's characters: read as words, and not understood.
	for _, text := range []string{"a b c d e", "0 3 * * * extra"} {
		if _, err := Parse(text); !errors.Is(err, ErrUnreadable) {
			t.Errorf("Parse(%q) error = %v, want ErrUnreadable", text, err)
		}
	}
	// 02:31 every day is a line like any other.
	if got, err := Parse("31 2 * * *"); err != nil || got != "31 2 * * *" {
		t.Errorf("Parse(31 2 * * *) = %q, %v", got, err)
	}
}

func TestValidate(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		cron string
		want string // empty: valid; else a part of the error
	}{
		{"* * * * *", ""},
		{"*/15 * * * *", ""},
		{"0 3 * * *", ""},
		{"0 0 1 1 *", ""},
		{"31 2 * * *", ""},            // 02:31 every day
		{"0 0 29 2 *", "never comes"}, // 29 February 2028 is over a year away
		{"0 0 31 2 *", "never comes"},
		{"0 0 31 4 *", "never comes"},
		{"0 0 * * * *", "five fields"},
		{"* * * *", "five fields"},
		{"", "five fields"},
		{"*/30 * * * * *", "five fields"},
		{"60 * * * *", "can't read"},
		{"x * * * *", "can't read"},
	} {
		err := validateAt(tc.cron, now)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("validateAt(%q) = %v, want valid", tc.cron, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("validateAt(%q) = %v, want %q", tc.cron, err, tc.want)
		}
	}
	// The exported one uses the real clock.
	if err := Validate("*/5 * * * *"); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if err := Validate("0 0 * * * *"); err == nil || err.Error() != fieldsMessage {
		t.Errorf("Validate(six fields) = %v", err)
	}
}

func TestNext(t *testing.T) {
	after := time.Date(2026, 10, 23, 14, 7, 30, 0, time.UTC) // a Friday
	for _, tc := range []struct {
		cron string
		n    int
		want []string
	}{
		{"*/15 * * * *", 3, []string{"2026-10-23T14:15:00Z", "2026-10-23T14:30:00Z", "2026-10-23T14:45:00Z"}},
		{"0 3 * * *", 2, []string{"2026-10-24T03:00:00Z", "2026-10-25T03:00:00Z"}},
		{"0 9 * * 1-5", 3, []string{"2026-10-26T09:00:00Z", "2026-10-27T09:00:00Z", "2026-10-28T09:00:00Z"}},
		{"0 6 1 * *", 2, []string{"2026-11-01T06:00:00Z", "2026-12-01T06:00:00Z"}},
		{"0 * * * *", 1, []string{"2026-10-23T15:00:00Z"}},
		{"0 * * * *", 0, nil},
		{"not a line", 3, nil},
	} {
		got := Next(tc.cron, after, tc.n)
		if len(got) != len(tc.want) {
			t.Errorf("Next(%q, %d) = %v, want %v", tc.cron, tc.n, got, tc.want)
			continue
		}
		for i, g := range got {
			if g.Format(time.RFC3339) != tc.want[i] || g.Location() != time.UTC {
				t.Errorf("Next(%q)[%d] = %v, want %s UTC", tc.cron, i, g, tc.want[i])
			}
		}
	}

	// A time given in another zone is read as the same instant, and the answers
	// stay in UTC: 14:07 UTC is 16:07 in +02:00.
	plus2 := time.FixedZone("plus2", 2*3600)
	got := Next("0 3 * * *", after.In(plus2), 1)
	if len(got) != 1 || got[0].Format(time.RFC3339) != "2026-10-24T03:00:00Z" {
		t.Errorf("Next in another zone = %v", got)
	}
}
