package logline

import (
	"strings"
	"testing"
	"unsafe"
)

func TestStripANSI(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"tab kept", "a\tb", "a\tb"},
		{"empty", "", ""},
		{"utf8 kept", "café © ñ ✓", "café © ñ ✓"},
		{"traefik sgr", "\x1b[90m2026-10-25T00:26:49Z\x1b[0m \x1b[31mERR\x1b[0m error=\"boom\"", "2026-10-25T00:26:49Z ERR error=\"boom\""},
		{"bold and color", "\x1b[1m\x1b[36mINF\x1b[0m started", "INF started"},
		{"sgr with many params", "\x1b[38;2;255;0;0mred\x1b[0m", "red"},
		{"cursor and erase", "a\x1b[2K\x1b[1Gb\x1b[10;20Hc", "abc"},
		{"csi with intermediate", "a\x1b[0 qb", "ab"},
		{"osc 8 hyperlink with ST", "\x1b]8;;https://example.com/x\x1b\\click\x1b]8;;\x1b\\ here", "click here"},
		{"osc 8 hyperlink with BEL", "\x1b]8;;https://example.com\x07click\x1b]8;;\x07", "click"},
		{"osc 52 clipboard write", "before\x1b]52;c;SGVsbG8gd29ybGQ=\x07after", "beforeafter"},
		{"osc title", "\x1b]0;my title\x07ready", "ready"},
		{"dcs to ST", "a\x1bPqsixel data\x1b\\b", "ab"},
		{"sos pm apc", "a\x1bXsos\x1b\\b\x1b^pm\x1b\\c\x1b_apc\x1b\\d", "abcd"},
		{"two byte escape", "a\x1b7b\x1b=c\x1bMd", "abcd"},
		{"charset selection", "a\x1b(Bb", "ab"},
		{"unterminated csi", "keep\x1b[31", "keep"},
		{"unterminated osc", "keep\x1b]0;title with no end", "keep"},
		{"unterminated dcs", "keep\x1bPdata", "keep"},
		{"lone escape at the end", "keep\x1b", "keep"},
		{"escape then control", "a\x1b\x01b", "ab"},
		{"csi cut by a control byte", "a\x1b[3\x01b", "ab"},
		{"nul", "a\x00b", "ab"},
		{"bell and backspace", "a\x07b\x08c", "abc"},
		{"del", "a\x7fb", "ab"},
		{"c1 csi", "\u009b31mred\u009b0m", "red"},
		{"c1 osc", "x\u009d0;title\u0007y", "xy"},
		{"c1 osc with c1 ST", "x\u009d0;title\u009cy", "xy"},
		{"c1 dcs", "x\u0090data\u009cy", "xy"},
		{"c1 plain", "a\u0085b\u0086c", "abc"},
		{"nbsp is not c1", "a b", "a b"},
		{"progress bar keeps the last", "10%\r20%\r30%", "30%"},
		{"progress bar with trailing cr", "10%\r20%\r30%\r", "30%"},
		{"trailing cr only", "done\r", "done"},
		{"crlf", "done\r\n", "done"},
		{"cr and color", "\x1b[32m10%\x1b[0m\r\x1b[32m20%\x1b[0m", "20%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripANSI(tc.in); got != tc.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestStripANSIFastPath checks a line with nothing to remove comes back as
// the very same string, not a copy.
func TestStripANSIFastPath(t *testing.T) {
	for _, in := range []string{
		"",
		"plain line",
		"a\ttab and unicode é ✓",
		`192.168.5.15 - - [25/Oct/2026:00:33:21 +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7"`,
	} {
		if needsStrip(in) {
			t.Errorf("needsStrip(%q) = true, want the fast path", in)
		}
		got := StripANSI(in)
		if got != in || (len(in) > 0 && unsafe.StringData(got) != unsafe.StringData(in)) {
			t.Errorf("StripANSI(%q) = %q, want the same string", in, got)
		}
	}
	for _, in := range []string{"a\x1b[0m", "a\rb", "a\x00", "a\x7f", "a b"} {
		if !needsStrip(in) {
			t.Errorf("needsStrip(%q) = false, want the slow path", in)
		}
	}
}

func TestStripANSINeverLeavesControls(t *testing.T) {
	// Every byte value alone and after ESC: nothing below 0x20 but tab, and
	// no DEL, may survive.
	for b := 0; b < 256; b++ {
		for _, in := range []string{
			"x" + string([]byte{byte(b)}) + "y",
			"x\x1b" + string([]byte{byte(b)}) + "y",
			"x\x1b[" + string([]byte{byte(b)}) + "y",
			"x\x1b]" + string([]byte{byte(b)}) + "y",
		} {
			got := StripANSI(in)
			for i := 0; i < len(got); i++ {
				if c := got[i]; (c < 0x20 && c != '\t') || c == 0x7f {
					t.Fatalf("StripANSI(%q) = %q holds control byte %#x", in, got, c)
				}
			}
			if !strings.HasPrefix(got, "x") && b != '\r' {
				t.Fatalf("StripANSI(%q) = %q lost the text before the escape", in, got)
			}
		}
	}
}
