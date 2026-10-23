// Package cronwords reads and writes the "When" of a schedule: a few plain
// phrases ("every 15 minutes", "weekdays at 09:00") that become a five-field
// cron line, and the inverse for showing a stored line. It also validates
// cron lines and lists their next run times. It is pure: no I/O, and no clock
// except where a function takes one.
//
// Every time of day is UTC, as a backup's schedule is.
package cronwords

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/cronexpr"
)

// ErrUnreadable is what Parse returns for text that is neither a cron line nor
// one of the phrases. Its text is a sentence for the person, shown beside the
// field, so it starts with a capital and ends with a full stop (which Go's
// error-string rule, ST1005, forbids for errors.New; a string type is fine).
var ErrUnreadable error = sentence(`Couldn't read that. Try "every 2 hours" or paste a cron line.`)

// sentence is an error whose text is a sentence for the person.
type sentence string

func (e sentence) Error() string { return string(e) }

const (
	// minGap is the shortest time between two runs of a schedule.
	minGap = time.Minute
	// horizon is how far ahead a cron line's next run may be.
	horizon = 366 * 24 * time.Hour
	// gapChecks is how many consecutive gaps Validate compares.
	gapChecks = 8
)

const fieldsMessage = "a cron line has five fields: minute, hour, day, month, weekday"

const nameList = "sun|mon|tue|wed|thu|fri|sat|jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec"

var (
	// symbolField is a cron field written with digits and cron's symbols.
	symbolField = regexp.MustCompile(`^[0-9*/,\-?LW#]+$`)
	// nameField is a cron field written with day or month names ("mon-fri").
	nameField = regexp.MustCompile(`(?i)^(` + nameList + `)([,\-](` + nameList + `))*$`)

	everyMinutes = regexp.MustCompile(`^every (\d+) minutes?$`)
	everyHours   = regexp.MustCompile(`^every (\d+) hours?$`)
	everyDay     = regexp.MustCompile(`^every day at (\d{1,2}):(\d{2})$`)
	weekdaysAt   = regexp.MustCompile(`^weekdays at (\d{1,2}):(\d{2})$`)
	weekdayAt    = regexp.MustCompile(`^(monday|tuesday|wednesday|thursday|friday|saturday|sunday)s? at (\d{1,2}):(\d{2})$`)
	firstOfMonth = regexp.MustCompile(`^first of the month(?: at (\d{1,2}):(\d{2}))?$`)
)

// weekdayNames are the cron weekday numbers' names, Sunday being 0.
var weekdayNames = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

// Parse turns what the person typed into a five-field cron line. A cron line
// (five or more fields written in cron's own characters) is checked with
// Validate and returned with its spaces normalised; otherwise the text is read
// as one of these phrases, in any case:
//
//	every minute            every N minutes (N divides 60)
//	every hour              every N hours (N divides 24)
//	every day at HH:MM      weekdays at HH:MM      mondays at HH:MM ... sundays at HH:MM
//	first of the month [at HH:MM]   (06:00 when no time is given)
//
// Anything else is ErrUnreadable.
func Parse(text string) (string, error) {
	fields := strings.Fields(text)
	cron := strings.Join(fields, " ")
	if len(fields) >= 5 && allCronFields(fields) {
		if err := Validate(cron); err != nil {
			return "", err
		}
		return cron, nil
	}
	cron, ok := fromWords(strings.ToLower(cron))
	if !ok {
		return "", ErrUnreadable
	}
	if err := Validate(cron); err != nil {
		return "", err
	}
	return cron, nil
}

// allCronFields reports whether every field is written in cron's characters.
func allCronFields(fields []string) bool {
	for _, f := range fields {
		if !symbolField.MatchString(f) && !nameField.MatchString(f) {
			return false
		}
	}
	return true
}

// fromWords reads one of the phrases (already lower case, single-spaced).
func fromWords(s string) (string, bool) {
	switch s {
	case "every minute":
		return "* * * * *", true
	case "every hour":
		return "0 * * * *", true
	}
	if m := everyMinutes.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || n > 59 || 60%n != 0 {
			return "", false
		}
		if n == 1 {
			return "* * * * *", true
		}
		return fmt.Sprintf("*/%d * * * *", n), true
	}
	if m := everyHours.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || n > 23 || 24%n != 0 {
			return "", false
		}
		if n == 1 {
			return "0 * * * *", true
		}
		return fmt.Sprintf("0 */%d * * *", n), true
	}
	if m := everyDay.FindStringSubmatch(s); m != nil {
		return timed(m[1], m[2], "* * *")
	}
	if m := weekdaysAt.FindStringSubmatch(s); m != nil {
		return timed(m[1], m[2], "* * 1-5")
	}
	if m := weekdayAt.FindStringSubmatch(s); m != nil {
		for d, name := range weekdayNames {
			if name == m[1] {
				return timed(m[2], m[3], "* * "+strconv.Itoa(d))
			}
		}
	}
	if m := firstOfMonth.FindStringSubmatch(s); m != nil {
		if m[1] == "" {
			return "0 6 1 * *", true
		}
		return timed(m[1], m[2], "1 * *")
	}
	return "", false
}

// timed is "minute hour <rest>" for a time of day, false when it isn't one.
func timed(hh, mm, rest string) (string, bool) {
	h, errH := strconv.Atoi(hh)
	m, errM := strconv.Atoi(mm)
	if errH != nil || errM != nil || h > 23 || m > 59 {
		return "", false
	}
	return fmt.Sprintf("%d %d %s", m, h, rest), true
}

// Words says a cron line the way Parse reads it ("Every 15 minutes", "Weekdays
// at 09:00 UTC"). A line those phrases don't cover is "On `<cron>`", which the
// console draws in mono. A time of day always ends " UTC".
func Words(cron string) string {
	f := strings.Fields(cron)
	other := "On `" + strings.Join(f, " ") + "`"
	if len(f) != 5 {
		return other
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	rest := dom + " " + month + " " + dow

	// Steps through the hour or the day.
	switch {
	case minute == "*" && hour == "*" && rest == "* * *":
		return "Every minute"
	case hour == "*" && rest == "* * *":
		if n, ok := stepOf(minute, 60); ok {
			return fmt.Sprintf("Every %d minutes", n)
		}
		if minute == "0" {
			return "Every hour"
		}
		return other
	case minute == "0" && rest == "* * *":
		if n, ok := stepOf(hour, 24); ok {
			return fmt.Sprintf("Every %d hours", n)
		}
	}

	// A time of day, on some days.
	h, errH := plain(hour, 23)
	m, errM := plain(minute, 59)
	if errH != nil || errM != nil || month != "*" {
		return other
	}
	at := fmt.Sprintf("%02d:%02d UTC", h, m)
	switch {
	case dom == "*" && dow == "*":
		return "Every day at " + at
	case dom == "*" && dow == "1-5":
		return "Weekdays at " + at
	case dom == "*" && len(dow) == 1 && dow[0] >= '0' && dow[0] <= '7':
		name := weekdayNames[int(dow[0]-'0')%7]
		return strings.ToUpper(name[:1]) + name[1:] + "s at " + at
	case dom == "1" && dow == "*":
		return "First of the month at " + at
	}
	return other
}

// stepOf reads "*/N" where N is at least 2 and divides whole.
func stepOf(field string, whole int) (int, bool) {
	rest, ok := strings.CutPrefix(field, "*/")
	if !ok {
		return 0, false
	}
	n, err := plain(rest, whole)
	if err != nil || n < 2 || n >= whole || whole%n != 0 {
		return 0, false
	}
	return n, true
}

// plain reads a field that is only digits (no sign, no leading zeros beyond
// what a time of day has), at most limit.
func plain(field string, limit int) (int, error) {
	if field == "" || strings.Trim(field, "0123456789") != "" {
		return 0, errors.New("not a plain number")
	}
	n, err := strconv.Atoi(field)
	if err != nil || n > limit {
		return 0, errors.New("not a plain number")
	}
	return n, nil
}

// Validate checks a cron line a schedule may keep: exactly five fields that
// cron can read, a next run within a year, and runs at least a minute apart.
// The error says what is wrong in plain words.
func Validate(cron string) error {
	return validateAt(cron, time.Now())
}

func validateAt(cron string, now time.Time) error {
	if len(strings.Fields(cron)) != 5 {
		return errors.New(fieldsMessage)
	}
	expr, err := cronexpr.Parse(cron)
	if err != nil {
		return fmt.Errorf("cron can't read that line: %w", err)
	}
	t := expr.Next(now.UTC())
	if t.IsZero() || t.After(now.Add(horizon)) {
		return errors.New("that time never comes: the line has no run in the next year")
	}
	for i := 0; i < gapChecks; i++ {
		next := expr.Next(t)
		if next.IsZero() {
			break
		}
		if next.Sub(t) < minGap {
			return errors.New("that line runs more than once a minute; the shortest gap is one minute")
		}
		t = next
	}
	return nil
}

// Next is the next n times cron fires after the given time, in UTC. It returns
// fewer when the line has fewer, and nothing when it can't be read.
func Next(cron string, after time.Time, n int) []time.Time {
	expr, err := cronexpr.Parse(cron)
	if err != nil || n <= 0 {
		return nil
	}
	var out []time.Time
	t := after.UTC()
	for len(out) < n {
		t = expr.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out
}
