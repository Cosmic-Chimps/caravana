package logline

// Parse is the whole per-line pass: strip the escapes, find the time, decide
// the level, and settle the time of a line that has none. The service calls it
// once per line, oldest first within a stream, passing the line before it.
//
//   - id and stream are the caller's; Parse copies them.
//   - b are the bounds for this line's file (see Bounds).
//   - prev is the previous line of the same stream, nil for the first. A line
//     with no time of its own (or one outside b) takes prev's time, marked
//     Estimated; the first line takes the copy's start, which is b.NotBefore
//     plus Slack (zero when b has no start). A continuation line takes prev's
//     level.
//
// A JSON line keeps the stripped object in Raw and shows its message as Text.
func Parse(raw string, stream Stream, id LineID, b Bounds, prev *Line) Line {
	text := StripANSI(raw)
	l := Line{ID: id, Stream: stream}

	t, span, ok := TimeOf(text, b)
	switch {
	case ok:
		l.Time, l.TimeSource = t, FromLine
	case prev != nil:
		l.Time, l.TimeSource = prev.Time, Estimated
	case !b.NotBefore.IsZero():
		l.Time, l.TimeSource = b.NotBefore.Add(Slack), Estimated
	default:
		l.TimeSource = Estimated
	}

	var prevLevel Level
	if prev != nil {
		prevLevel = prev.Level
	}
	level, msg, isJSON := LevelOf(cut(text, span), prevLevel)
	if isJSON {
		// A JSON line's time is inside a string: LevelOf got the whole object.
		l.Level, l.Text, l.Raw = level, msg, text
		return l
	}
	l.Level, l.Text, l.span = level, text, span
	return l
}

// Fold joins consecutive lines whose text, with the time cut out, is
// byte-equal: the group becomes the newest line (its ID, Time and the rest)
// with Repeat counting the lines it replaced. lines are one stream's, oldest
// first; lines of another stream or copy never fold into each other. The
// input is not changed.
func Fold(lines []Line) []Line {
	if len(lines) == 0 {
		return nil
	}
	out := make([]Line, 0, len(lines))
	var last string // TextWithoutTime of out's last line
	for _, l := range lines {
		text := TextWithoutTime(l)
		if n := len(out); n > 0 {
			p := &out[n-1]
			if text == last && p.Stream == l.Stream && p.ID.Copy == l.ID.Copy {
				l.Repeat += p.Repeat + 1
				*p = l
				continue
			}
		}
		out = append(out, l)
		last = text
	}
	return out
}
