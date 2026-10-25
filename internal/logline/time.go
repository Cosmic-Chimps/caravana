package logline

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// TimeOf reads the time a line carries, from the shapes apps really write, at
// the places they write them: nothing else is searched, so a date inside a
// message ("expires 2026-11-01 10:00:00") is never taken as the line's time.
// s is the line after StripANSI.
//
// The places, in order:
//   - a JSON object's time, ts, timestamp, @timestamp, @t or t (a string in
//     any shape below, or a number: seconds under 1e11, milliseconds above);
//   - klog, "I1025 00:33:21.123456";
//   - the start of the line, or after one short token of at most 8 bytes
//     ("1:M", "I,", "<34>1", "[123]", "INFO"), or inside a leading "[" (gunicorn,
//     Rails): RFC 3339 / ISO 8601 with "T" or a space, a "." or "," fraction
//     and Z, an offset, UTC, GMT or nothing; Go's log, "2006/01/02 15:04:05";
//     Redis, "23 Oct 2026 23:13:18.730"; syslog, "Oct 25 00:33:21";
//   - the Common Log Format bracket after exactly three words,
//     "[25/Oct/2026:00:33:21 +0000]";
//   - time=, ts= or timestamp= among the first six logfmt pairs.
//
// A time with no zone is UTC. A time with no year (syslog, klog) takes the
// year, this one or the one before, that puts it inside b. A time before
// b.NotBefore or after b.NotAfter (a side that is zero is not checked) is
// refused: ok is false and the caller marks the line estimated.
//
// The Span is where the time sits in s, for TextWithoutTime and the level
// reader. A JSON line has no Span: its time is inside a string.
func TimeOf(s string, b Bounds) (t time.Time, span Span, ok bool) {
	t, span, ok = find(s, b)
	if !ok {
		return time.Time{}, Span{}, false
	}
	t = t.UTC()
	if !b.contains(t) {
		return time.Time{}, Span{}, false
	}
	return t, span, true
}

func find(s string, b Bounds) (time.Time, Span, bool) {
	if s == "" {
		return time.Time{}, Span{}, false
	}
	if s[0] == '{' {
		t, ok := jsonTime(s, b)
		return t, Span{}, ok
	}
	if t, end, ok := klogAt(s, b); ok {
		return t, Span{1, end}, true
	}
	starts, n := startPositions(s)
	for _, p := range starts[:n] {
		if t, sp, ok := stampOrBracket(s, p, b); ok {
			return t, sp, true
		}
	}
	if t, sp, ok := clfAt(s); ok {
		return t, sp, true
	}
	return logfmtTime(s, b)
}

// startPositions lists where a time may begin near the start: the start, after
// a "<34>" syslog priority, and after one short token following either.
func startPositions(s string) (out [4]int, n int) {
	add := func(p int) {
		for k := 0; k < n; k++ {
			if out[k] == p {
				return
			}
		}
		out[n] = p
		n++
	}
	bases := [2]int{0, -1}
	if e := priorityEnd(s); e > 0 {
		bases[1] = e
	}
	for _, base := range bases {
		if base < 0 {
			continue
		}
		add(base)
		if q, ok := afterToken(s, base); ok {
			add(q)
		}
	}
	return out, n
}

// priorityEnd is the index after a syslog priority like "<34>", or 0.
func priorityEnd(s string) int {
	if len(s) < 3 || s[0] != '<' {
		return 0
	}
	for i := 1; i < len(s) && i <= 4; i++ {
		switch {
		case s[i] >= '0' && s[i] <= '9':
		case s[i] == '>' && i > 1:
			return i + 1
		default:
			return 0
		}
	}
	return 0
}

// afterToken skips one token of 1 to 8 bytes and the spaces after it. The
// token must look like a tag ("1:M", "I,", "<34>1", "[1]": it holds a digit or
// punctuation) or be a level word ("INFO 2026-…"). A plain word ("expires",
// "since") isn't a prefix, so a date that follows one in a message never
// becomes the line's time.
func afterToken(s string, p int) (int, bool) {
	sp := strings.IndexByte(s[p:], ' ')
	if sp < 1 || sp > 8 {
		return 0, false
	}
	token := s[p : p+sp]
	if _, isLevel := wordLevel(token); !isLevel &&
		!strings.ContainsFunc(token, func(r rune) bool { return !unicode.IsLetter(r) }) {
		return 0, false
	}
	q := p + sp
	for q < len(s) && s[q] == ' ' {
		q++
	}
	if q >= len(s) {
		return 0, false
	}
	return q, true
}

// stampOrBracket reads a time at p, or inside a "[" at p. When the bracket
// holds only the time, the Span takes both brackets.
func stampOrBracket(s string, p int, b Bounds) (time.Time, Span, bool) {
	if t, end, ok := stampAt(s, p, b); ok {
		return t, Span{p, end}, true
	}
	if p < len(s) && s[p] == '[' {
		if t, end, ok := stampAt(s, p+1, b); ok {
			if end < len(s) && s[end] == ']' {
				return t, Span{p, end + 1}, true
			}
			return t, Span{p + 1, end}, true
		}
	}
	return time.Time{}, Span{}, false
}

// stampAt reads one of the date shapes that begin at s[i], and returns where
// it ends.
func stampAt(s string, i int, b Bounds) (time.Time, int, bool) {
	if i >= len(s) {
		return time.Time{}, 0, false
	}
	c := s[i]
	switch {
	case c >= '0' && c <= '9':
		if t, n, ok := parseISO(s[i:]); ok {
			return t, i + n, true
		}
		if t, n, ok := parseRedis(s[i:]); ok {
			return t, i + n, true
		}
	case c >= 'A' && c <= 'Z':
		if t, n, ok := parseSyslog(s[i:], b); ok {
			return t, i + n, true
		}
	}
	return time.Time{}, 0, false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// at is s[i], or 0 past the end.
func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

// num reads n digits at s[i:].
func num(s string, i, n int) (int, bool) {
	if i < 0 || i+n > len(s) {
		return 0, false
	}
	v := 0
	for k := 0; k < n; k++ {
		c := s[i+k]
		if !isDigit(c) {
			return 0, false
		}
		v = v*10 + int(c-'0')
	}
	return v, true
}

// parseClock reads "15:04:05" with an optional "." or "," fraction at s[i:].
func parseClock(s string, i int) (h, m, sec, ns, end int, ok bool) {
	if i < 0 || i+8 > len(s) || s[i+2] != ':' || s[i+5] != ':' {
		return
	}
	var ok1, ok2, ok3 bool
	h, ok1 = num(s, i, 2)
	m, ok2 = num(s, i+3, 2)
	sec, ok3 = num(s, i+6, 2)
	if !ok1 || !ok2 || !ok3 || h > 23 || m > 59 || sec > 59 {
		return
	}
	end = i + 8
	if (at(s, end) == '.' || at(s, end) == ',') && isDigit(at(s, end+1)) {
		k := end + 1
		digits := 0
		for k < len(s) && isDigit(s[k]) {
			if digits < 9 {
				ns = ns*10 + int(s[k]-'0')
				digits++
			}
			k++
		}
		for ; digits < 9; digits++ {
			ns *= 10
		}
		end = k
	}
	return h, m, sec, ns, end, true
}

// mkTime builds a UTC time and refuses a date that doesn't exist.
func mkTime(y, mo, d, h, m, s, ns int) (time.Time, bool) {
	if y < 1970 || y > 2200 || mo < 1 || mo > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(mo), d, h, m, s, ns, time.UTC)
	if t.Day() != d || int(t.Month()) != mo {
		return time.Time{}, false
	}
	return t, true
}

// parseISO reads "2026-10-25T00:26:49Z", "2026-10-24 23:48:36.479 UTC",
// "2026/10/25 00:16:42", "2026-10-25 00:33:21,123" and the like.
func parseISO(s string) (time.Time, int, bool) {
	if len(s) < 19 {
		return time.Time{}, 0, false
	}
	sep := s[4]
	if (sep != '-' && sep != '/') || s[7] != sep || (s[10] != 'T' && s[10] != ' ') {
		return time.Time{}, 0, false
	}
	y, ok1 := num(s, 0, 4)
	mo, ok2 := num(s, 5, 2)
	d, ok3 := num(s, 8, 2)
	if !ok1 || !ok2 || !ok3 {
		return time.Time{}, 0, false
	}
	h, m, sec, ns, end, ok := parseClock(s, 11)
	if !ok {
		return time.Time{}, 0, false
	}
	t, ok := mkTime(y, mo, d, h, m, sec, ns)
	if !ok {
		return time.Time{}, 0, false
	}
	off, end := parseZone(s, end)
	return t.Add(-off), end, true
}

// parseZone reads an optional zone after a clock: Z, an offset glued to the
// time (+01:00, +0100, +01) or after one space (+0000, +01:00), or a space and
// UTC or GMT. With none it returns a zero offset and i.
func parseZone(s string, i int) (time.Duration, int) {
	switch c := at(s, i); {
	case c == 'Z':
		if !isAlnum(at(s, i+1)) {
			return 0, i + 1
		}
	case c == '+' || c == '-':
		if off, end, ok := parseOffset(s, i, false); ok {
			return off, end
		}
	case c == ' ':
		d := at(s, i+1)
		if d == '+' || d == '-' {
			if off, end, ok := parseOffset(s, i+1, true); ok {
				return off, end
			}
		} else if strings.HasPrefix(s[i+1:], "UTC") || strings.HasPrefix(s[i+1:], "GMT") {
			if !isLetter(at(s, i+4)) {
				return 0, i + 4
			}
		}
	}
	return 0, i
}

func isAlnum(c byte) bool { return isDigit(c) || isLetter(c) }

// parseOffset reads +hh:mm, +hhmm, or (when not spaced) +hh at s[i:].
func parseOffset(s string, i int, spaced bool) (time.Duration, int, bool) {
	hh, ok := num(s, i+1, 2)
	if !ok || hh > 23 {
		return 0, 0, false
	}
	mm, end := 0, i+3
	switch {
	case at(s, i+3) == ':':
		v, ok := num(s, i+4, 2)
		if !ok || v > 59 {
			return 0, 0, false
		}
		mm, end = v, i+6
	default:
		if v, ok := num(s, i+3, 2); ok {
			if v > 59 {
				return 0, 0, false
			}
			mm, end = v, i+5
		} else if spaced {
			return 0, 0, false
		}
	}
	if isDigit(at(s, end)) {
		return 0, 0, false
	}
	off := time.Duration(hh*3600+mm*60) * time.Second
	if s[i] == '-' {
		off = -off
	}
	return off, end, true
}

// month reads a three-letter month name ("Oct") at s[0:3] and returns 1 to 12.
func month(s string) int {
	if len(s) < 3 {
		return 0
	}
	switch s[:3] {
	case "Jan":
		return 1
	case "Feb":
		return 2
	case "Mar":
		return 3
	case "Apr":
		return 4
	case "May":
		return 5
	case "Jun":
		return 6
	case "Jul":
		return 7
	case "Aug":
		return 8
	case "Sep":
		return 9
	case "Oct":
		return 10
	case "Nov":
		return 11
	case "Dec":
		return 12
	}
	return 0
}

// day reads one or two digits at s[i:] and returns the number and the index
// after it.
func day(s string, i int) (int, int, bool) {
	if !isDigit(at(s, i)) {
		return 0, i, false
	}
	if isDigit(at(s, i+1)) {
		d, _ := num(s, i, 2)
		return d, i + 2, true
	}
	return int(s[i] - '0'), i + 1, true
}

// parseRedis reads "23 Oct 2026 23:13:18.730".
func parseRedis(s string) (time.Time, int, bool) {
	d, i, ok := day(s, 0)
	if !ok || at(s, i) != ' ' {
		return time.Time{}, 0, false
	}
	mo := month(s[i+1:])
	if mo == 0 || at(s, i+4) != ' ' {
		return time.Time{}, 0, false
	}
	y, ok := num(s, i+5, 4)
	if !ok || at(s, i+9) != ' ' {
		return time.Time{}, 0, false
	}
	h, m, sec, ns, end, ok := parseClock(s, i+10)
	if !ok {
		return time.Time{}, 0, false
	}
	t, ok := mkTime(y, mo, d, h, m, sec, ns)
	return t, end, ok
}

// parseSyslog reads "Oct 25 00:33:21" or "Oct  5 00:33:21", which carry no
// year.
func parseSyslog(s string, b Bounds) (time.Time, int, bool) {
	mo := month(s)
	if mo == 0 || at(s, 3) != ' ' {
		return time.Time{}, 0, false
	}
	i := 4
	if at(s, i) == ' ' {
		i++
	}
	d, i, ok := day(s, i)
	if !ok || at(s, i) != ' ' {
		return time.Time{}, 0, false
	}
	h, m, sec, ns, end, ok := parseClock(s, i+1)
	if !ok {
		return time.Time{}, 0, false
	}
	t, ok := inferYear(mo, d, h, m, sec, ns, b)
	return t, end, ok
}

// klogAt reads "I1025 00:33:21.123456 ..." at the start. The Span it
// returns starts after the level letter.
func klogAt(s string, b Bounds) (time.Time, int, bool) {
	if len(s) < 15 {
		return time.Time{}, 0, false
	}
	switch s[0] {
	case 'I', 'W', 'E', 'F':
	default:
		return time.Time{}, 0, false
	}
	mo, ok1 := num(s, 1, 2)
	d, ok2 := num(s, 3, 2)
	if !ok1 || !ok2 || s[5] != ' ' {
		return time.Time{}, 0, false
	}
	h, m, sec, ns, end, ok := parseClock(s, 6)
	if !ok {
		return time.Time{}, 0, false
	}
	t, ok := inferYear(mo, d, h, m, sec, ns, b)
	return t, end, ok
}

// inferYear gives a time that has no year the year, this one or the one
// before, that puts it inside b. The reference is the end of the bounds (the
// file's ModTime), else the start. With neither it can't tell and says no.
func inferYear(mo, d, h, m, sec, ns int, b Bounds) (time.Time, bool) {
	ref := b.NotAfter
	if ref.IsZero() && !b.NotBefore.IsZero() {
		ref = b.NotBefore.Add(Slack)
	}
	if ref.IsZero() {
		return time.Time{}, false
	}
	y := ref.UTC().Year()
	for _, yy := range [2]int{y, y - 1} {
		if t, ok := mkTime(yy, mo, d, h, m, sec, ns); ok && b.contains(t) {
			return t, true
		}
	}
	return time.Time{}, false
}

// clfAt reads the Common Log Format bracket after exactly three words:
// `192.168.5.15 - - [25/Oct/2026:00:33:21 +0000] "GET / ..."`. The Span takes
// both brackets.
func clfAt(s string) (time.Time, Span, bool) {
	p := 0
	for w := 0; w < 3; w++ {
		sp := strings.IndexByte(s[p:], ' ')
		if sp < 1 {
			return time.Time{}, Span{}, false
		}
		p += sp
		for p < len(s) && s[p] == ' ' {
			p++
		}
	}
	if at(s, p) != '[' {
		return time.Time{}, Span{}, false
	}
	t, n, ok := parseCLF(s[p+1:])
	if !ok || at(s, p+1+n) != ']' {
		return time.Time{}, Span{}, false
	}
	return t, Span{p, p + 1 + n + 1}, true
}

// parseCLF reads "25/Oct/2026:00:33:21 +0000".
func parseCLF(s string) (time.Time, int, bool) {
	d, i, ok := day(s, 0)
	if !ok || at(s, i) != '/' {
		return time.Time{}, 0, false
	}
	mo := month(s[i+1:])
	if mo == 0 || at(s, i+4) != '/' {
		return time.Time{}, 0, false
	}
	y, ok := num(s, i+5, 4)
	if !ok || at(s, i+9) != ':' {
		return time.Time{}, 0, false
	}
	h, m, sec, ns, end, ok := parseClock(s, i+10)
	if !ok {
		return time.Time{}, 0, false
	}
	t, ok := mkTime(y, mo, d, h, m, sec, ns)
	if !ok {
		return time.Time{}, 0, false
	}
	off, end := parseZone(s, end)
	return t.Add(-off), end, true
}

// valueTime reads a time from a field's value (a logfmt value or a JSON
// string): one of the date shapes, or a number of seconds or milliseconds.
func valueTime(v string, b Bounds) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	if t, _, ok := stampAt(v, 0, b); ok {
		return t, true
	}
	if t, _, ok := parseCLF(v); ok {
		return t, true
	}
	for i := 0; i < len(v); i++ {
		if !isDigit(v[i]) && v[i] != '.' {
			return time.Time{}, false
		}
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return time.Time{}, false
	}
	return unixTime(f)
}

// unixTime reads a number as seconds when it is under 1e11 and as
// milliseconds above, with a fraction allowed (rounded to the microsecond: a
// float holds no more). Anything before 2000 or after
// about 2100 is not a time.
func unixTime(f float64) (time.Time, bool) {
	switch {
	case f >= 946684800 && f < 1e11:
		sec := math.Floor(f)
		return time.Unix(int64(sec), int64((f-sec)*1e9)).Round(time.Microsecond).UTC(), true
	case f >= 1e11 && f < 4.2e12:
		ms := math.Floor(f)
		return time.UnixMilli(int64(ms)).Add(time.Duration((f - ms) * 1e6)).Round(time.Microsecond).UTC(), true
	}
	return time.Time{}, false
}

// timeKeys are a JSON object's time fields, the first present and readable
// one wins.
var timeKeys = [...]string{"time", "ts", "timestamp", "@timestamp", "@t", "t"}

func jsonTime(s string, b Bounds) (time.Time, bool) {
	m, ok := parseObject(s)
	if !ok {
		return time.Time{}, false
	}
	for _, k := range timeKeys {
		raw, present := m[k]
		if !present || len(raw) == 0 {
			continue
		}
		if raw[0] == '"' {
			var str string
			if json.Unmarshal(raw, &str) == nil {
				if t, ok := valueTime(str, b); ok {
					return t, true
				}
			}
			continue
		}
		if f, err := strconv.ParseFloat(string(raw), 64); err == nil {
			if t, ok := unixTime(f); ok {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// maxJSONLine is the longest line read as JSON: the line limit.
const maxJSONLine = 64 << 10

// parseObject reads s as one JSON object, or says no.
func parseObject(s string) (map[string]json.RawMessage, bool) {
	if len(s) < 2 || len(s) > maxJSONLine || s[0] != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

// logfmtTime finds time=, ts= or timestamp= among the first six pairs.
func logfmtTime(s string, b Bounds) (t time.Time, sp Span, ok bool) {
	logfmtPairs(s, 6, func(key, val string, start, end int) bool {
		switch key {
		case "time", "ts", "timestamp":
		default:
			return false
		}
		v, found := valueTime(val, b)
		if !found {
			return false
		}
		t, sp, ok = v, Span{start, end}, true
		return true
	})
	return t, sp, ok
}

// logfmtPairs walks up to max key=value pairs from the start of s, calling fn
// with the key, the value (quotes removed, escapes kept) and the pair's byte
// range. It stops when fn returns true or when the text stops looking like
// logfmt (a word that is not a pair).
func logfmtPairs(s string, max int, fn func(key, val string, start, end int) bool) {
	i := 0
	for n := 0; n < max; n++ {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		start := i
		for i < len(s) && s[i] != '=' && s[i] != ' ' && s[i] != '"' {
			i++
		}
		if i >= len(s) || s[i] != '=' || i == start {
			return
		}
		key := s[start:i]
		i++
		var val string
		if at(s, i) == '"' {
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j > len(s) {
				j = len(s)
			}
			val = s[i+1 : j]
			i = j
			if i < len(s) {
				i++ // the closing quote
			}
		} else {
			j := i
			for j < len(s) && s[j] != ' ' {
				j++
			}
			val = s[i:j]
			i = j
		}
		if fn(key, val, start, i) {
			return
		}
	}
}
