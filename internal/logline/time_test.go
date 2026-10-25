package logline

import (
	"testing"
	"time"
)

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("bad test time %q: %v", s, err)
	}
	return v.UTC()
}

// wide holds every example of the research: a copy that started on
// 2026-10-23 and whose file was last written on 2026-10-25 at one o'clock.
var wide = Bounds{
	NotBefore: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
	NotAfter:  time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
}

// TestTimeOfShapes holds every shape of research R2: the line, the time it
// carries and the text the span covers.
func TestTimeOfShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want string // RFC 3339
		span string // the bytes the span covers; "" for JSON (no span)
	}{
		// The lab's own lines, first.
		{"traefik rfc3339 Z", `2026-10-25T00:26:49Z ERR error="middleware does not exist" entryPointName=websecure`, "2026-10-25T00:26:49Z", "2026-10-25T00:26:49Z"},
		{"postgres zone word", `2026-10-24 23:48:36.479 UTC [1] LOG:  database system is ready to accept connections`, "2026-10-24T23:48:36.479Z", "2026-10-24 23:48:36.479 UTC"},
		{"go log no zone", `2026/10/25 00:16:42 Starting up on port 80`, "2026-10-25T00:16:42Z", "2026/10/25 00:16:42"},
		{"nginx error log", `2026/10/23 23:13:18 [notice] 1#1: start worker processes`, "2026-10-23T23:13:18Z", "2026/10/23 23:13:18"},
		{"rclone", `2026/10/25 00:01:33 NOTICE: Failed to copyto: boom`, "2026-10-25T00:01:33Z", "2026/10/25 00:01:33"},
		{"nginx access clf", `192.168.5.15 - - [25/Oct/2026:00:33:21 +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7"`, "2026-10-25T00:33:21Z", "[25/Oct/2026:00:33:21 +0000]"},
		{"redis after the pid prefix", `1:M 23 Oct 2026 23:13:18.730 * Ready to accept connections tcp`, "2026-10-23T23:13:18.73Z", "23 Oct 2026 23:13:18.730"},
		{"redis warning", `1:M 23 Oct 2026 23:13:18.804 # WARNING: Redis does not require authentication`, "2026-10-23T23:13:18.804Z", "23 Oct 2026 23:13:18.804"},

		// The rest of the shapes table.
		{"python comma fraction", `2026-10-25 00:33:21,123 INFO something happened`, "2026-10-25T00:33:21.123Z", "2026-10-25 00:33:21,123"},
		{"iso with a space and no zone", `2026-10-25 00:33:21 hello`, "2026-10-25T00:33:21Z", "2026-10-25 00:33:21"},
		{"iso with microseconds", `2026-10-25T00:33:21.123456 hello`, "2026-10-25T00:33:21.123456Z", "2026-10-25T00:33:21.123456"},
		{"iso with nanoseconds", `2026-10-25T00:33:21.123456789Z hello`, "2026-10-25T00:33:21.123456789Z", "2026-10-25T00:33:21.123456789Z"},
		{"iso with a colon offset", `2026-10-25T02:26:49+02:00 hello`, "2026-10-25T00:26:49Z", "2026-10-25T02:26:49+02:00"},
		{"iso with a negative offset", `2026-10-24T19:26:49-05:00 hello`, "2026-10-25T00:26:49Z", "2026-10-24T19:26:49-05:00"},
		{"iso with a short offset", `2026-10-25T02:26:49+02 hello`, "2026-10-25T00:26:49Z", "2026-10-25T02:26:49+02"},
		{"iso with GMT", `2026-10-25 00:33:21 GMT hello`, "2026-10-25T00:33:21Z", "2026-10-25 00:33:21 GMT"},
		{"gunicorn bracket", `[2026-10-25 00:33:21 +0000] [1] [INFO] Booting worker with pid: 7`, "2026-10-25T00:33:21Z", "[2026-10-25 00:33:21 +0000]"},
		{"gunicorn bracket with an offset", `[2026-10-25 02:33:21 +0200] [1] [INFO] Booting`, "2026-10-25T00:33:21Z", "[2026-10-25 02:33:21 +0200]"},
		{"rails bracket", `I, [2026-10-25T00:33:21.123456 #1]  INFO -- : Started GET "/"`, "2026-10-25T00:33:21.123456Z", "2026-10-25T00:33:21.123456"},
		{"pid in brackets prefix", `[1234] 2026-10-25 00:33:21 worker started`, "2026-10-25T00:33:21Z", "2026-10-25 00:33:21"},
		{"level word prefix", `INFO 2026-10-25 00:33:21 worker started`, "2026-10-25T00:33:21Z", "2026-10-25 00:33:21"},
		{"logfmt time", `time=2026-10-25T00:33:21.123Z level=INFO msg="hello world"`, "2026-10-25T00:33:21.123Z", "time=2026-10-25T00:33:21.123Z"},
		{"logfmt ts", `ts=2026-10-25T00:33:21Z caller=main.go:12 level=info msg=started`, "2026-10-25T00:33:21Z", "ts=2026-10-25T00:33:21Z"},
		{"logfmt timestamp quoted", `level=info timestamp="2026-10-25 00:33:21" msg=started`, "2026-10-25T00:33:21Z", `timestamp="2026-10-25 00:33:21"`},
		{"logfmt float seconds", `ts=1792886809.1 level=info msg=started`, "2026-10-25T00:06:49.1Z", "ts=1792886809.1"},
		{"logfmt fifth pair", `a=1 b=2 c=3 d=4 time=2026-10-25T00:33:21Z msg=late`, "2026-10-25T00:33:21Z", "time=2026-10-25T00:33:21Z"},
		{"json time string", `{"time":"2026-10-25T00:33:21Z","level":"info","msg":"x"}`, "2026-10-25T00:33:21Z", ""},
		{"json ts float seconds (zap)", `{"level":"error","ts":1792886809.1,"msg":"dial tcp: refused"}`, "2026-10-25T00:06:49.1Z", ""},
		{"json time milliseconds (pino)", `{"level":30,"time":1792886809123,"msg":"x"}`, "2026-10-25T00:06:49.123Z", ""},
		{"json timestamp", `{"timestamp":"2026-10-25T00:33:21.5Z","msg":"x"}`, "2026-10-25T00:33:21.5Z", ""},
		{"json @timestamp", `{"@timestamp":"2026-10-25T00:33:21Z","message":"x"}`, "2026-10-25T00:33:21Z", ""},
		{"json @t (serilog)", `{"@t":"2026-10-25T00:33:21Z","@mt":"Started {Id}","Id":3}`, "2026-10-25T00:33:21Z", ""},
		{"json t", `{"t":"2026-10-25T00:33:21Z","msg":"x"}`, "2026-10-25T00:33:21Z", ""},
		{"json first readable key wins", `{"time":"not a time","ts":1792886809,"msg":"x"}`, "2026-10-25T00:06:49Z", ""},
		{"json string in another shape", `{"time":"2026/10/25 00:33:21","msg":"x"}`, "2026-10-25T00:33:21Z", ""},
		{"syslog", `Oct 25 00:33:21 host sshd[1]: accepted`, "2026-10-25T00:33:21Z", "Oct 25 00:33:21"},
		{"syslog with a priority", `<34>Oct 25 00:33:21 host x: y`, "2026-10-25T00:33:21Z", "Oct 25 00:33:21"},
		{"syslog 5424 priority and version", `<34>1 2026-10-25T00:33:21Z host app - - - msg`, "2026-10-25T00:33:21Z", "2026-10-25T00:33:21Z"},
		{"klog", `E1025 00:33:21.123456       1 reflector.go:138] failed to list`, "2026-10-25T00:33:21.123456Z", "1025 00:33:21.123456"},
		{"glog info", `I1025 00:33:21.5 7 x.go:1] hi`, "2026-10-25T00:33:21.5Z", "1025 00:33:21.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, span, ok := TimeOf(tc.line, wide)
			if !ok {
				t.Fatalf("TimeOf(%q) = not ok", tc.line)
			}
			if want := ts(t, tc.want); !got.Equal(want) {
				t.Errorf("time = %s, want %s", got.Format(time.RFC3339Nano), tc.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("location = %v, want UTC", got.Location())
			}
			if tc.span == "" {
				if span != (Span{}) {
					t.Errorf("span = %v, want none", span)
				}
				return
			}
			if span.Start < 0 || span.End > len(tc.line) || span.Start >= span.End {
				t.Fatalf("span %v is out of range of the line", span)
			}
			if text := tc.line[span.Start:span.End]; text != tc.span {
				t.Errorf("span covers %q, want %q", text, tc.span)
			}
		})
	}
}

func TestTimeOfNoTime(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"empty", ""},
		{"no time", "Starting up on port 80"},
		{"error text", `Error handling is disabled for /health`},
		{"date in the middle of a message", `Certificate will expire on 2026-11-01 10:00:00 UTC`},
		{"date after a long prefix", `Certificate expires 2026-11-01 10:00:00`},
		{"date after two words", `Started at 2026-10-25 00:33:21 by the owner`},
		{"date only", `2026-10-25 hello`},
		{"clf after four words", `a b c d [25/Oct/2026:00:33:21 +0000] "GET /"`},
		{"clf after two words", `a b [25/Oct/2026:00:33:21 +0000] "GET /"`},
		{"clf in the middle of a path", `GET /x?d=[25/Oct/2026:00:33:21 +0000] 200`},
		{"logfmt time past the sixth pair", `a=1 b=2 c=3 d=4 e=5 f=6 time=2026-10-25T00:33:21Z`},
		{"logfmt time not a time", `time=soon level=info`},
		{"json without a time", `{"level":"info","msg":"x"}`},
		{"json with an unreadable time", `{"time":"soon","msg":"x"}`},
		{"json number too small", `{"t":5,"msg":"x"}`},
		{"json broken", `{"time":"2026-10-25T00:33:21Z"`},
		{"month 13", `2026-13-25 00:33:21 hello`},
		{"day 31 of a short month", `2026-11-31 00:33:21 hello`},
		{"hour 24", `2026-10-25 24:33:21 hello`},
		{"syslog month name only", `Octopus 25 00:33:21 x`},
		{"almost klog", `Info 1025 00:33:21 x`},
		{"indented", `  2026-10-25 00:33:21 hello`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, span, ok := TimeOf(tc.line, wide); ok {
				t.Errorf("TimeOf(%q) = %s %v, want no time", tc.line, got.Format(time.RFC3339), span)
			}
		})
	}
}

// TestTimeOfNoZoneIsUTC: Go's log, nginx's error log, Redis and Python write
// no zone, and they are read as UTC whatever the machine's own zone.
func TestTimeOfNoZoneIsUTC(t *testing.T) {
	for _, line := range []string{
		`2026/10/25 00:16:42 x`,
		`2026-10-25 00:16:42 x`,
		`2026-10-25 00:16:42,000 x`,
		`1:M 25 Oct 2026 00:16:42.000 * x`,
		`Oct 25 00:16:42 host x`,
		`time=2026-10-25T00:16:42 level=info`,
	} {
		got, _, ok := TimeOf(line, wide)
		if !ok {
			t.Errorf("TimeOf(%q) = not ok", line)
			continue
		}
		if want := ts(t, "2026-10-25T00:16:42Z"); !got.Equal(want) {
			t.Errorf("TimeOf(%q) = %s, want %s", line, got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}
}

func TestTimeOfBounds(t *testing.T) {
	start := ts(t, "2026-10-25T00:00:00Z")
	b := Bounds{NotBefore: start.Add(-Slack), NotAfter: ts(t, "2026-10-25T01:00:00Z")}

	for _, tc := range []struct {
		name string
		line string
		ok   bool
	}{
		{"inside", `2026-10-25T00:30:00Z x`, true},
		{"at the start", `2026-10-25T00:00:00Z x`, true},
		{"thirty seconds before the start (the slack)", `2026-10-24T23:59:30Z x`, true},
		{"exactly at the lower bound", `2026-10-24T23:59:00Z x`, true},
		{"an hour before the copy started", `2026-10-24T23:00:00Z x`, false},
		{"a minute and a second before", `2026-10-24T23:58:59Z x`, false},
		{"after the file's last write", `2026-10-25T01:00:01Z x`, false},
		{"exactly at the file's last write", `2026-10-25T01:00:00Z x`, true},
		{"a local zone read as UTC (two hours ahead)", `2026-10-25T02:30:00 x`, false},
		{"json an hour before", `{"time":"2026-10-24T23:00:00Z","msg":"x"}`, false},
		{"json number after the file's last write", `{"ts":1792890409,"msg":"x"}`, false}, // 01:06:49
		{"json number inside", `{"ts":1792886809,"msg":"x"}`, true},                       // 00:06:49
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := TimeOf(tc.line, b)
			if ok != tc.ok {
				t.Errorf("TimeOf(%q) ok = %v, want %v", tc.line, ok, tc.ok)
			}
		})
	}

	t.Run("a bound that is zero is not checked", func(t *testing.T) {
		line := `2020-01-01T00:00:00Z x`
		if _, _, ok := TimeOf(line, Bounds{}); !ok {
			t.Error("no bounds: want ok")
		}
		if _, _, ok := TimeOf(line, Bounds{NotAfter: b.NotAfter}); !ok {
			t.Error("only an upper bound: want ok")
		}
		if _, _, ok := TimeOf(line, Bounds{NotBefore: b.NotBefore}); ok {
			t.Error("a lower bound after the time: want not ok")
		}
	})
}

func TestTimeOfYearless(t *testing.T) {
	for _, tc := range []struct {
		name   string
		line   string
		bounds Bounds
		want   string // "" for not ok
	}{
		{"this year", `Oct 25 00:33:21 host x`, wide, "2026-10-25T00:33:21Z"},
		{"the year before, across new year",
			`Dec 31 23:59:00 host x`,
			Bounds{NotBefore: ts(t, "2026-12-31T00:00:00Z"), NotAfter: ts(t, "2027-01-01T00:10:00Z")},
			"2026-12-31T23:59:00Z"},
		{"klog the year before",
			`E1231 23:59:00.5       1 x.go:1] boom`,
			Bounds{NotBefore: ts(t, "2026-12-31T00:00:00Z"), NotAfter: ts(t, "2027-01-01T00:10:00Z")},
			"2026-12-31T23:59:00.5Z"},
		{"single digit day", `Oct  5 00:33:21 host x`, Bounds{NotBefore: ts(t, "2026-10-05T00:00:00Z"), NotAfter: ts(t, "2026-10-06T00:00:00Z")}, "2026-10-05T00:33:21Z"},
		{"inside neither year", `Jun 1 00:00:00 host x`, wide, ""},
		{"february 29 only in a leap year", `Feb 29 12:00:00 host x`,
			Bounds{NotBefore: ts(t, "2028-02-28T00:00:00Z"), NotAfter: ts(t, "2028-03-02T00:00:00Z")}, "2028-02-29T12:00:00Z"},
		{"no bounds can't tell the year", `Oct 25 00:33:21 host x`, Bounds{}, ""},
		{"only a start", `Oct 25 00:33:21 host x`, Bounds{NotBefore: ts(t, "2026-10-24T23:59:00Z")}, "2026-10-25T00:33:21Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, ok := TimeOf(tc.line, tc.bounds)
			if tc.want == "" {
				if ok {
					t.Fatalf("TimeOf(%q) = %s, want not ok", tc.line, got.Format(time.RFC3339Nano))
				}
				return
			}
			if !ok {
				t.Fatalf("TimeOf(%q) = not ok, want %s", tc.line, tc.want)
			}
			if want := ts(t, tc.want); !got.Equal(want) {
				t.Errorf("time = %s, want %s", got.Format(time.RFC3339Nano), tc.want)
			}
		})
	}
}

func TestTextWithoutTime(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{`2026-10-25T00:26:49Z ERR boom`, `ERR boom`},
		{`2026-10-24 23:48:36.479 UTC [1] LOG:  ready`, `[1] LOG:  ready`},
		{`192.168.5.15 - - [25/Oct/2026:00:33:21 +0000] "GET / HTTP/1.1" 200`, `192.168.5.15 - -  "GET / HTTP/1.1" 200`},
		{`1:M 23 Oct 2026 23:13:18.730 * Ready`, `1:M  * Ready`},
		{`time=2026-10-25T00:33:21Z level=INFO msg=x`, `level=INFO msg=x`},
		{`[2026-10-25 00:33:21 +0000] [1] [INFO] Booting`, `[1] [INFO] Booting`},
		{`no time here`, `no time here`},
	} {
		l := Parse(tc.line, Stdout, LineID{}, wide, nil)
		if got := TextWithoutTime(l); got != tc.want {
			t.Errorf("TextWithoutTime(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}
