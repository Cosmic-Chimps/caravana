package logline

import (
	"encoding/json"
	"strconv"
	"strings"
)

// words maps the level words apps write (lowercase) to a level.
var words = map[string]Level{
	"error": Error, "err": Error, "eror": Error, "fatal": Error, "ftl": Error,
	"panic": Error, "pnc": Error, "crit": Error, "critical": Error, "alert": Error,
	"emerg": Error, "emergency": Error, "fail": Error, "severe": Error,

	"warn": Warn, "warning": Warn, "wrn": Warn, "wrng": Warn,

	"info": Info, "inf": Info, "information": Info, "notice": Info, "log": Info, "ok": Info,

	"debug": Debug, "dbg": Debug, "dbug": Debug, "trace": Debug, "trc": Debug,
	"trce": Debug, "verbose": Debug, "fine": Debug, "finer": Debug, "finest": Debug,
	"debug1": Debug, "debug2": Debug, "debug3": Debug, "debug4": Debug, "debug5": Debug,
}

// wordLevel looks a word up, in any case.
func wordLevel(w string) (Level, bool) {
	if len(w) > 12 {
		return "", false
	}
	var buf [12]byte
	for i := 0; i < len(w); i++ {
		c := w[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf[i] = c
	}
	l, ok := words[string(buf[:len(w)])]
	return l, ok
}

// LevelOf decides a line's level from its text.
//
// text is the stripped line with the time already cut out (TextWithoutTime
// does it), except for a JSON line, whose time is a field: pass that whole.
// prev is the level of the line before it in the same stream, which an
// indented continuation line inherits (empty: none yet, so info). The stream
// is never read: an unmatched line is info on stdout and stderr alike.
//
// The order:
//  1. A JSON object (at most 64 KiB): its level, lvl, severity, LogLevel, @l or
//     log.level (a word, or a pino/bunyan number: 10 trace, 20 debug, 30 info,
//     40 warn, 50 error, 60 fatal; no @l in a Serilog line is info, as is no
//     level at all). msg is its msg, message, Message, @m or @mt, else text.
//  2. The explicit prefixes: klog/glog "E1025 ", Rails "E, [", the .NET
//     console's "fail: ", and the sentences "panic:", "fatal error:",
//     "Traceback (most recent call last):" and "Exception in thread".
//  3. level=, lvl= or severity= among the first six logfmt pairs.
//  4. The first four words (surrounding [] <> (), a trailing ":" or ",", and
//     anything from the first ":" removed; words with no letter, like "[1]" or
//     "#", are skipped and not counted) against the table of level words.
//  5. A continuation (starts with a space or tab, "at ", "Caused by:",
//     "... N more", or is a Postgres DETAIL, HINT, CONTEXT or STATEMENT line)
//     takes prev.
//  6. Info.
//
// Step 2 comes before the words, unlike the research's list: a klog line's
// own letter is more reliable than an "error" a few words into its message.
func LevelOf(text string, prev Level) (level Level, msg string, isJSON bool) {
	if m, ok := parseObject(text); ok {
		return jsonLevel(m), jsonMessage(m, text), true
	}
	if l, ok := prefixLevel(text); ok {
		return l, text, false
	}
	if l, ok := logfmtLevel(text); ok {
		return l, text, false
	}
	l, first, colon, found := firstWords(text)
	if found {
		return l, text, false
	}
	if isContinuation(text, first, colon) {
		if prev == "" {
			return Info, text, false
		}
		return prev, text, false
	}
	return Info, text, false
}

// levelKeys are a JSON object's level fields, in order.
var levelKeys = [...]string{"level", "lvl", "severity", "LogLevel", "@l", "log.level"}

// messageKeys are a JSON object's message fields, in order.
var messageKeys = [...]string{"msg", "message", "Message", "@m", "@mt"}

func jsonLevel(m map[string]json.RawMessage) Level {
	for _, k := range levelKeys {
		raw, ok := m[k]
		if !ok && k == "log.level" {
			raw, ok = nestedLevel(m)
		}
		if !ok {
			continue
		}
		if l, ok := rawLevel(raw); ok {
			return l
		}
	}
	return Info // no level, or Serilog's absent @l
}

// nestedLevel reads {"log":{"level":...}}, the form ECS writes.
func nestedLevel(m map[string]json.RawMessage) (json.RawMessage, bool) {
	rawLog, ok := m["log"]
	if !ok {
		return nil, false
	}
	var inner map[string]json.RawMessage
	if json.Unmarshal(rawLog, &inner) != nil {
		return nil, false
	}
	raw, ok := inner["level"]
	return raw, ok
}

func rawLevel(raw json.RawMessage) (Level, bool) {
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		s = strings.TrimSpace(s)
		if l, ok := wordLevel(s); ok {
			return l, true
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "", false
		}
		return numberLevel(f), true
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return "", false
	}
	return numberLevel(f), true
}

// numberLevel reads pino and bunyan's numbers: 10 trace, 20 debug, 30 info,
// 40 warn, 50 error, 60 fatal. A number between two is the lower one's level.
func numberLevel(f float64) Level {
	switch {
	case f >= 50:
		return Error
	case f >= 40:
		return Warn
	case f >= 30:
		return Info
	}
	return Debug
}

func jsonMessage(m map[string]json.RawMessage, text string) string {
	for _, k := range messageKeys {
		raw, ok := m[k]
		if !ok || len(raw) == 0 || raw[0] != '"' {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	return text
}

// prefixLevel reads the prefixes that say the level outright.
func prefixLevel(text string) (Level, bool) {
	if l, ok := klogLevel(text); ok {
		return l, true
	}
	if len(text) >= 4 && text[1] == ',' && text[2] == ' ' && text[3] == '[' {
		// Rails: "I, [2026-10-25T00:33:21.123456 #1]  INFO -- : ..."
		switch text[0] {
		case 'D':
			return Debug, true
		case 'I':
			return Info, true
		case 'W':
			return Warn, true
		case 'E', 'F':
			return Error, true
		}
	}
	if len(text) >= 6 && text[4] == ':' && text[5] == ' ' {
		switch text[:4] {
		case "trce", "dbug":
			return Debug, true
		case "info":
			return Info, true
		case "warn":
			return Warn, true
		case "fail", "crit":
			return Error, true
		}
	}
	for _, p := range [...]string{"panic:", "fatal error:", "Traceback (most recent call last):", "Exception in thread"} {
		if strings.HasPrefix(text, p) {
			return Error, true
		}
	}
	return "", false
}

// klogLevel reads a klog or glog line's letter: "E1025 ..." as written, or as
// it reads once its time is cut out, "E       1 reflector.go:138] ...".
func klogLevel(text string) (Level, bool) {
	if len(text) < 2 {
		return "", false
	}
	var l Level
	switch text[0] {
	case 'I':
		l = Info
	case 'W':
		l = Warn
	case 'E', 'F':
		l = Error
	default:
		return "", false
	}
	if _, ok := num(text, 1, 4); ok && at(text, 5) == ' ' {
		return l, true
	}
	// The cut form: spaces, a thread id, spaces, "file.go:12]".
	i := 1
	for i < len(text) && text[i] == ' ' {
		i++
	}
	if i == 1 {
		return "", false
	}
	j := i
	for j < len(text) && isDigit(text[j]) {
		j++
	}
	if j == i || at(text, j) != ' ' {
		return "", false
	}
	for j < len(text) && text[j] == ' ' {
		j++
	}
	end := strings.IndexByte(text[j:], ' ')
	if end < 0 {
		end = len(text) - j
	}
	tok := text[j : j+end]
	if len(tok) < 4 || tok[len(tok)-1] != ']' || !strings.Contains(tok, ":") {
		return "", false
	}
	return l, true
}

// logfmtLevel finds level=, lvl= or severity= among the first six pairs.
func logfmtLevel(text string) (l Level, ok bool) {
	logfmtPairs(text, 6, func(key, val string, _, _ int) bool {
		if !strings.EqualFold(key, "level") && !strings.EqualFold(key, "lvl") && !strings.EqualFold(key, "severity") {
			return false
		}
		l, ok = wordLevel(val)
		return ok
	})
	return l, ok
}

// firstWords looks the first four words up. It also reports the first word
// counted and whether a ":" followed it, for the continuation check.
func firstWords(text string) (level Level, first string, colon, found bool) {
	counted := 0
	for i := 0; i < len(text) && counted < 4; {
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		start := i
		for i < len(text) && text[i] != ' ' && text[i] != '\t' {
			i++
		}
		tok := text[start:i]
		if tok == "" {
			break
		}
		w := strings.TrimRight(strings.TrimLeft(tok, "[<("), "]>),:")
		hadColon := strings.Contains(tok, ":")
		if c := strings.IndexByte(w, ':'); c >= 0 {
			w = w[:c]
		}
		if !hasLetter(w) {
			continue
		}
		counted++
		if counted == 1 {
			first, colon = w, hadColon
		}
		if l, ok := wordLevel(w); ok {
			return l, first, colon, true
		}
	}
	return "", first, colon, false
}

func hasLetter(s string) bool {
	for i := 0; i < len(s); i++ {
		if isLetter(s[i]) {
			return true
		}
	}
	return false
}

// isContinuation says a line belongs to the one before it: it is indented, or
// is a stack trace's "at ", "Caused by:" or "... 12 more", or a Postgres
// DETAIL, HINT, CONTEXT or STATEMENT line (first is its first counted word,
// which sits after "[1]" once the time is cut).
func isContinuation(text, first string, colon bool) bool {
	if text == "" {
		return false
	}
	if text[0] == ' ' || text[0] == '\t' {
		return true
	}
	if strings.HasPrefix(text, "at ") || strings.HasPrefix(text, "Caused by:") {
		return true
	}
	if strings.HasPrefix(text, "... ") {
		i := 4
		for i < len(text) && isDigit(text[i]) {
			i++
		}
		if i > 4 && strings.HasPrefix(text[i:], " more") {
			return true
		}
	}
	if colon {
		switch first {
		case "DETAIL", "HINT", "CONTEXT", "STATEMENT":
			return true
		}
	}
	return false
}
