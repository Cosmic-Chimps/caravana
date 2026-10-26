package logline

import (
	"fmt"
	"testing"
	"time"
)

// nginxLines makes n lines of nginx's access log, as the lab saw it: a health
// check every few seconds and the odd request between.
func nginxLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		sec := i % 60
		if i%7 == 0 {
			lines[i] = fmt.Sprintf(`10.0.%d.%d - - [25/Oct/2026:00:%02d:%02d +0000] "GET /static/app-%d.js HTTP/1.1" 200 %d "https://example.com/" "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36"`,
				i%250, i%200, (i/60)%60, sec, i%9, 1000+i%5000)
			continue
		}
		lines[i] = fmt.Sprintf(`192.168.5.15 - - [25/Oct/2026:00:%02d:%02d +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7 health check"`, (i/60)%60, sec)
	}
	return lines
}

// parseAll is the service's loop over one stream: strip, time, level, in order.
func parseAll(lines []string, b Bounds) []Line {
	out := make([]Line, 0, len(lines))
	var prev *Line
	for i, raw := range lines {
		l := Parse(raw, Stdout, LineID{Copy: copyA, Stream: Stdout, Offset: int64(i)}, b, prev)
		out = append(out, l)
		prev = &out[len(out)-1]
	}
	return out
}

// TestParse100kNginxLines is the measured pass: a hundred thousand lines of
// nginx's access log shape, parsed and folded, well inside a second. The limit
// here is loose (the race detector slows it several times over); the
// benchmark below holds the real one.
func TestParse100kNginxLines(t *testing.T) {
	if testing.Short() {
		t.Skip("timing check")
	}
	lines := nginxLines(100_000)
	start := time.Now()
	parsed := parseAll(lines, wide)
	folded := Fold(parsed)
	elapsed := time.Since(start)
	if elapsed > 4*time.Second {
		t.Errorf("parsing 100k lines took %s, want well under a second", elapsed)
	}
	if len(folded) >= len(parsed) {
		t.Errorf("nothing folded: %d rows from %d lines", len(folded), len(parsed))
	}
	for _, l := range parsed[:50] {
		if l.TimeSource != FromLine {
			t.Fatalf("line %q has no time of its own", l.Text)
		}
	}
}

// BenchmarkParse100kNginxLines parses and folds 100,000 lines per iteration
// and fails when one iteration takes a second or more.
func BenchmarkParse100kNginxLines(b *testing.B) {
	lines := nginxLines(100_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_ = Fold(parseAll(lines, wide))
		if d := time.Since(start); d >= time.Second {
			b.Fatalf("100k lines took %s, want under 1s", d)
		}
	}
}

func BenchmarkStripANSIPlain(b *testing.B) {
	line := nginxLines(1)[0]
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		_ = StripANSI(line)
	}
}

func BenchmarkStripANSIColored(b *testing.B) {
	line := "\x1b[90m2026-10-25T00:26:49Z\x1b[0m \x1b[31mERR\x1b[0m error=\"middleware does not exist\" entryPointName=websecure"
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		_ = StripANSI(line)
	}
}

func BenchmarkTimeOfNoTime(b *testing.B) {
	line := "Starting up on port 80 and listening for connections from everywhere on this host"
	for i := 0; i < b.N; i++ {
		_, _, _ = TimeOf(line, wide)
	}
}
