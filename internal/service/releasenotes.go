package service

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// ReleaseNote is one line of a release's notes as the Updates page draws it. A bullet with a bold
// lead ("**MCP**: `deploy_image` is marked destructive. More.") is Lead "MCP" (without its `**`),
// Colon true (the bullet had its colon right after the lead) and Text the first sentence of what
// follows ("`deploy_image` is marked destructive."); a bullet without a lead is only Text, its first
// sentence. Lead and Text may hold backticks, which a page sets in its mono face; no other Markdown
// is read. A lead with nothing after it has an empty Text.
type ReleaseNote struct {
	Lead  string
	Colon bool
	Text  string
}

// String is the note as one line: "MCP: `deploy_image` is ...". It is what the API's notes are.
func (n ReleaseNote) String() string {
	switch {
	case n.Lead == "":
		return n.Text
	case n.Text == "":
		return n.Lead
	case n.Colon:
		return n.Lead + ": " + n.Text
	}
	return n.Lead + " " + n.Text
}

// ParseReleaseNotes reads a release's notes, which are a CHANGELOG section in Markdown, into at most
// six lines. Each top-level bullet ("- " or "* ") is read whole: its wrapped lines are joined with
// single spaces, and a blank line, a heading, the next bullet or a nested bullet ends it. A bullet
// that starts with a bold lead ("**API 0.28.0**: ...") is its lead and the first sentence of the
// rest; any other is its first sentence. A sentence is never cut in the middle.
//
// cut says body was cut at the size the service keeps, so the bullet that was open at the end is
// partial and is dropped.
func ParseReleaseNotes(body string, cut bool) []ReleaseNote {
	if cut {
		// A cut at a line's end can't be told from a finished line: only a blank line or a heading
		// after a bullet says it is whole.
		body = strings.TrimSuffix(body, "\n")
	}
	bullets, openAtEnd := bulletsOf(body)
	if cut && openAtEnd {
		bullets = bullets[:len(bullets)-1]
	}
	var out []ReleaseNote
	for _, b := range bullets {
		if note, ok := noteOf(b); ok {
			out = append(out, note)
		}
		if len(out) == releaseNotesLines {
			break
		}
	}
	return out
}

// bulletsOf is the top-level bullets of a Markdown text, each joined into one line. openAtEnd says
// the last one runs to the end of the text, with nothing after it that closes it.
func bulletsOf(body string) (bullets []string, openAtEnd bool) {
	var cur []string
	open := false
	flush := func() {
		if len(cur) > 0 {
			bullets = append(bullets, strings.Join(cur, " "))
		}
		cur, open = nil, false
	}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimLeft(line, " \t")
		indented := len(trimmed) < len(line)
		switch {
		case trimmed == "":
			flush()
		case !indented && (strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ")):
			flush()
			cur, open = []string{strings.TrimSpace(trimmed[2:])}, true
		case !indented && strings.HasPrefix(trimmed, "#"):
			flush()
		case indented && (strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ")):
			// A nested bullet belongs to the one above and is not read.
			open = false
		case open:
			cur = append(cur, trimmed)
		}
	}
	openAtEnd = open
	flush()
	return bullets, openAtEnd
}

// noteOf is the line a bullet gives. ok is false for a bullet with nothing to say.
func noteOf(bullet string) (ReleaseNote, bool) {
	bullet = strings.TrimSpace(bullet)
	if rest, ok := strings.CutPrefix(bullet, "**"); ok {
		if lead, after, ok := strings.Cut(rest, "**"); ok && strings.TrimSpace(lead) != "" {
			lead = strings.TrimSpace(lead)
			after = strings.TrimSpace(after)
			// A colon right after the lead is the page's separator. A lead that ends in its own
			// punctuation, or one the text goes on from ("**Configure** says ..."), takes a space.
			colon := false
			if t, ok := strings.CutPrefix(after, ":"); ok {
				after, colon = strings.TrimSpace(t), !endsInPunctuation(lead)
			}
			text := firstSentence(dropBold(after))
			return ReleaseNote{Lead: lead, Colon: colon && text != "", Text: text}, true
		}
	}
	text := firstSentence(dropBold(bullet))
	return ReleaseNote{Text: text}, text != ""
}

// endsInPunctuation says a text ends with a stop, a colon, "!" or "?".
func endsInPunctuation(s string) bool {
	return s != "" && strings.ContainsRune(".:!?", rune(s[len(s)-1]))
}

// dropBold removes `**` marks outside backticks, keeping the words.
func dropBold(s string) string {
	var b strings.Builder
	inCode := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '`':
			inCode = !inCode
		case !inCode && strings.HasPrefix(s[i:], "**"):
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// abbreviations are words that end in a full stop without ending a sentence.
var abbreviations = []string{"e.g", "i.e", "etc", "vs", "approx", "no"}

// firstSentence is the text up to the end of its first sentence: a full stop, "!" or "?" outside
// backticks that is followed by a space or the end. A text without one is returned whole.
func firstSentence(s string) string {
	inCode := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '`' {
			inCode = !inCode
			continue
		}
		if inCode || (c != '.' && c != '!' && c != '?') {
			continue
		}
		if i+1 < len(s) && s[i+1] != ' ' {
			continue // "0.15.0", "a.b"
		}
		if c == '.' && endsInAbbreviation(s[:i]) {
			continue
		}
		return strings.TrimSpace(s[:i+1])
	}
	return strings.TrimSpace(s)
}

// endsInAbbreviation says the text ends with one of the abbreviations as a word of its own.
func endsInAbbreviation(s string) bool {
	word := s
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		word = s[i+1:]
	}
	return slices.Contains(abbreviations, strings.ToLower(word))
}

// notesCut says a body is as long as the service keeps, so its end may be partial. cutBytes may
// give up to three bytes of a character at the cut.
func notesCut(body string) bool { return len(body) > releaseBodyLimit-utf8.UTFMax }
