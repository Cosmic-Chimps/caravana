package logline

import "strings"

// StripANSI removes the terminal control sequences from one line of output
// and leaves the text a person would read.
//
// It removes CSI (colors, cursor moves), OSC (titles, hyperlinks, clipboard
// writes: ended by BEL or ST), DCS, SOS, PM and APC strings (ended by ST),
// the other two-byte escapes, the C1 forms written as UTF-8 (U+0080 to
// U+009F, with U+009B as CSI and U+009D as OSC), every other C0 control but
// tab, and DEL. A sequence that never ends is dropped to the end of the line.
// A carriage return inside the line keeps only what follows the last one, as
// a terminal draws a progress bar; a trailing one is dropped.
//
// Invalid UTF-8 is left as it is: the deploy reader already replaces it.
func StripANSI(s string) string {
	if !needsStrip(s) {
		return s
	}
	return strip(s)
}

// needsStrip is the fast path: no control byte but tab, no DEL and no 0xC2
// (the lead byte of U+0080 to U+009F), so there is nothing to remove.
func needsStrip(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\t') || c == 0x7f || c == 0xc2 {
			return true
		}
	}
	return false
}

func strip(s string) string {
	s = strings.TrimRight(s, "\r\n")
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		s = s[i+1:]
	}
	var sb strings.Builder
	sb.Grow(len(s))
	run := 0 // start of the text not yet written
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			sb.WriteString(s[run:i])
			i = skipEscape(s, i)
			run = i
		case c == 0xc2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f:
			sb.WriteString(s[run:i])
			i = skipC1(s, i, s[i+1])
			run = i
		case (c < 0x20 && c != '\t') || c == 0x7f:
			sb.WriteString(s[run:i])
			i++
			run = i
		default:
			i++
		}
	}
	sb.WriteString(s[run:])
	return sb.String()
}

// skipEscape returns the index after the escape sequence that starts at i.
func skipEscape(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return len(s)
	}
	switch c := s[j]; {
	case c == '[':
		return skipCSI(s, j+1)
	case c == ']':
		return skipString(s, j+1, true)
	case c == 'P' || c == 'X' || c == '^' || c == '_':
		return skipString(s, j+1, false)
	case c >= 0x20 && c <= 0x2f:
		// ESC, intermediates, a final byte: character set selection.
		k := j
		for k < len(s) && s[k] >= 0x20 && s[k] <= 0x2f {
			k++
		}
		if k >= len(s) {
			return len(s)
		}
		if s[k] >= 0x30 && s[k] <= 0x7e {
			return k + 1
		}
		return k
	case c >= 0x30 && c <= 0x7e:
		return j + 1
	}
	return j // a lone ESC: drop it, read on from the next byte
}

// skipCSI skips parameter bytes, intermediate bytes and the final byte. A byte
// that cannot belong ends the sequence before it.
func skipCSI(s string, j int) int {
	for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
		j++
	}
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
		j++
	}
	if j >= len(s) {
		return len(s)
	}
	if s[j] >= 0x40 && s[j] <= 0x7e {
		return j + 1
	}
	return j
}

// skipString skips a control string up to its terminator: ST (ESC \ or the
// UTF-8 form of U+009C), and also BEL when bel is true (OSC). A stray ESC ends
// it before the ESC, which is then read as a new sequence.
func skipString(s string, j int, bel bool) int {
	for j < len(s) {
		c := s[j]
		switch {
		case bel && c == 0x07:
			return j + 1
		case c == 0x1b:
			if j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
			return j
		case c == 0xc2 && j+1 < len(s) && s[j+1] == 0x9c:
			return j + 2
		}
		j++
	}
	return len(s)
}

// skipC1 skips the C1 control that starts at i (0xC2, then c in 0x80-0x9F).
func skipC1(s string, i int, c byte) int {
	switch c {
	case 0x9b:
		return skipCSI(s, i+2)
	case 0x9d:
		return skipString(s, i+2, true)
	case 0x90, 0x98, 0x9e, 0x9f:
		return skipString(s, i+2, false)
	}
	return i + 2
}
