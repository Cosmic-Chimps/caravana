package logline

import "testing"

// TestParseExamples holds the examples table of research R3: each line as the
// service reads it, the text left once its time is cut out, and its level. The
// first six are the lab's own lines.
func TestParseExamples(t *testing.T) {
	for _, tc := range []struct {
		name    string
		line    string
		stream  Stream
		timeCut string // the text of the line with its time cut out
		level   Level
	}{
		{"traefik ERR", "\x1b[90m2026-10-25T00:26:49Z\x1b[0m \x1b[31mERR\x1b[0m error=\"middleware \\\"x-auth@file\\\" does not exist\" routerName=web",
			Stdout, `ERR error="middleware \"x-auth@file\" does not exist" routerName=web`, Error},
		{"postgres LOG", `2026-10-24 23:48:36.479 UTC [1] LOG:  database system is ready`, Stdout, `[1] LOG:  database system is ready`, Info},
		{"nginx notice", `2026/10/23 23:13:18 [notice] 1#1: start worker processes`, Stderr, `[notice] 1#1: start worker processes`, Info},
		{"rclone NOTICE", `2026/10/25 00:01:33 NOTICE: Failed to copyto: boom`, Stderr, `NOTICE: Failed to copyto: boom`, Info},
		{"redis WARNING", `1:M 23 Oct 2026 23:13:18.804 # WARNING: Redis does not require authentication`, Stdout, `1:M  # WARNING: Redis does not require authentication`, Warn},
		{"whoami on stderr is not a warning", `2026/10/25 00:16:42 Starting up on port 80`, Stderr, `Starting up on port 80`, Info},

		{"nginx access", `192.168.5.15 - - [25/Oct/2026:00:33:21 +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7"`,
			Stdout, `192.168.5.15 - -  "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7"`, Info},
		{"zap json", `{"level":"error","ts":1792886809.1,"msg":"dial tcp: refused"}`, Stdout, `{"level":"error","ts":1792886809.1,"msg":"dial tcp: refused"}`, Error},
		{"serilog without @l", `{"@t":"2026-10-25T00:33:21Z","@mt":"Started {Id}","Id":3}`, Stdout, `{"@t":"2026-10-25T00:33:21Z","@mt":"Started {Id}","Id":3}`, Info},
		{"go slog", `time=2026-10-25T00:33:21Z level=WARN msg="slow query"`, Stdout, `level=WARN msg="slow query"`, Warn},
		{"klog", `E1025 00:33:21.123456       1 reflector.go:138] failed to list`, Stderr, `E       1 reflector.go:138] failed to list`, Error},
		{"gunicorn", `[2026-10-25 00:33:21 +0000] [1] [INFO] Booting worker with pid: 7`, Stderr, `[1] [INFO] Booting worker with pid: 7`, Info},
		{"python basicConfig", `WARNING:root:disk almost full`, Stderr, `WARNING:root:disk almost full`, Warn},
		{"error handling is a known false positive", `Error handling is disabled for /health`, Stdout, `Error handling is disabled for /health`, Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := Parse(tc.line, tc.stream, LineID{}, wide, nil)
			if l.Level != tc.level {
				t.Errorf("level = %q, want %q", l.Level, tc.level)
			}
			if got := TextWithoutTime(l); l.Raw == "" && got != tc.timeCut {
				t.Errorf("text without time = %q, want %q", got, tc.timeCut)
			}
			if l.Stream != tc.stream {
				t.Errorf("stream = %q, want %q", l.Stream, tc.stream)
			}
		})
	}
}

// TestParseDotNetContinuation is the .NET console's two-line format: the level
// on the first line, the message on the indented line after it.
func TestParseDotNetContinuation(t *testing.T) {
	first := Parse("fail: Microsoft.AspNetCore.Server.Kestrel[13]", Stdout, LineID{}, wide, nil)
	second := Parse(`      Connection id "0HN4F2QK1" communication error.`, Stdout, LineID{}, wide, &first)
	if first.Level != Error || second.Level != Error {
		t.Errorf("levels = %q, %q, want error, error", first.Level, second.Level)
	}
	// An info line followed by an indented one stays info.
	info := Parse("info: Microsoft.Hosting.Lifetime[14]", Stdout, LineID{}, wide, nil)
	next := Parse(`      Now listening on: http://[::]:8080`, Stdout, LineID{}, wide, &info)
	if info.Level != Info || next.Level != Info {
		t.Errorf("levels = %q, %q, want info, info", info.Level, next.Level)
	}
}

// TestLevelWords is the words table of R3, a few spellings of each word: bare,
// bracketed, followed by a colon, and in capitals.
func TestLevelWords(t *testing.T) {
	table := map[Level][]string{
		Error: {"error", "err", "eror", "fatal", "ftl", "panic", "pnc", "crit", "critical", "alert", "emerg", "emergency", "fail", "severe"},
		Warn:  {"warn", "warning", "wrn", "wrng"},
		Info:  {"info", "inf", "information", "notice", "log", "ok"},
		Debug: {"debug", "dbg", "dbug", "trace", "trc", "trce", "verbose", "debug1", "debug2", "debug3", "debug4", "debug5", "fine", "finer", "finest"},
	}
	for want, ws := range table {
		for _, w := range ws {
			for _, line := range []string{
				w + " something happened",
				"[" + w + "] something happened",
				"<" + w + "> something happened",
				"(" + w + ") something happened",
				w + ": something happened",
				w + ", something happened",
				upper(w) + " something happened",
				"[" + upper(w) + "]: something happened",
				"12 [3] # " + w + " something happened", // numbers and # are skipped, not counted
				"worker 7 started " + w + " now",        // the fourth word counts
			} {
				if got, _, isJSON := LevelOf(line, ""); got != want || isJSON {
					t.Errorf("LevelOf(%q) = %q, want %q", line, got, want)
				}
			}
		}
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func TestLevelOfWindow(t *testing.T) {
	for _, tc := range []struct {
		line string
		want Level
	}{
		{"one two three four error", Info},            // the fifth word is out of the window
		{"one two three error", Error},                // the fourth is in
		{"1 2 3 4 5 6 7 error", Error},                // numbers don't count
		{"a connection error occurred", Error},        // third word
		{"the service had a fatal crash", Info},       // fifth word
		{"retrying: error was transient", Error},      // second word
		{"errors were found", Info},                   // a word is the whole word
		{"warnings: none", Info},                      // "warnings" is not "warning"
		{"x=1 y=2 error", Error},                      // pairs are words too
		{"level=error msg=\"all ok now\"", Error},     // logfmt level first, before "ok"
		{"msg=\"all ok now\" level=debug", Debug},     // and anywhere in the first six pairs
		{"a=1 b=2 c=3 d=4 e=5 f=6 level=error", Info}, // but not past the sixth
		{"lvl=warn msg=x", Warn},
		{"severity=CRITICAL msg=x", Error},
		{"level=wat msg=x", Info},
		{"", Info},
	} {
		if got, _, _ := LevelOf(tc.line, ""); got != tc.want {
			t.Errorf("LevelOf(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestLevelOfPrefixes(t *testing.T) {
	for _, tc := range []struct {
		line string
		want Level
	}{
		// klog and glog, as written and with the time cut out.
		{"I1025 00:33:21.123456       1 main.go:1] started", Info},
		{"W1025 00:33:21.123456       1 main.go:1] slow", Warn},
		{"E1025 00:33:21.123456       1 main.go:1] bad", Error},
		{"F1025 00:33:21.123456       1 main.go:1] dead", Error},
		{"I       1 main.go:1] the error was ignored", Info}, // the letter beats the word
		{"W       7 x.go:9] ok", Warn},
		{"E       1 reflector.go:138] failed to list", Error},
		{"I think this is fine", Info},
		{"E 1 not a klog line", Info},
		// Rails.
		{"D, [ #1] DEBUG -- : sql", Debug},
		{"I, [ #1]  INFO -- : Started", Info},
		{"W, [ #1]  WARN -- : slow", Warn},
		{"E, [ #1] ERROR -- : boom", Error},
		{"F, [ #1] FATAL -- : dead", Error},
		// The .NET console.
		{"trce: A.B[0]", Debug},
		{"dbug: A.B[0]", Debug},
		{"info: A.B[0]", Info},
		{"warn: A.B[0]", Warn},
		{"fail: A.B[0]", Error},
		{"crit: A.B[0]", Error},
		// Sentences.
		{"panic: runtime error: index out of range", Error},
		{"fatal error: all goroutines are asleep - deadlock!", Error},
		{"Traceback (most recent call last):", Error},
		{`Exception in thread "main" java.lang.IllegalStateException: x`, Error},
	} {
		if got, _, _ := LevelOf(tc.line, ""); got != tc.want {
			t.Errorf("LevelOf(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestLevelOfJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want Level
		msg  string
	}{
		{"level string", `{"level":"warn","msg":"slow"}`, Warn, "slow"},
		{"level in capitals", `{"level":"ERROR","message":"boom"}`, Error, "boom"},
		{"lvl", `{"lvl":"dbug","msg":"x"}`, Debug, "x"},
		{"severity", `{"severity":"CRITICAL","message":"x"}`, Error, "x"},
		{"dotnet LogLevel", `{"LogLevel":"Information","Message":"Now listening"}`, Info, "Now listening"},
		{"dotnet warning", `{"LogLevel":"Warning","Message":"slow"}`, Warn, "slow"},
		{"serilog @l", `{"@t":"2026-10-25T00:33:21Z","@l":"Warning","@m":"slow"}`, Warn, "slow"},
		{"serilog without @l is information", `{"@t":"2026-10-25T00:33:21Z","@mt":"Started {Id}","Id":3}`, Info, "Started {Id}"},
		{"serilog error", `{"@t":"2026-10-25T00:33:21Z","@l":"Error","@mt":"Failed {Id}"}`, Error, "Failed {Id}"},
		{"ecs flat", `{"log.level":"error","message":"x"}`, Error, "x"},
		{"ecs nested", `{"log":{"level":"warn"},"message":"x"}`, Warn, "x"},
		{"pino trace", `{"level":10,"msg":"x"}`, Debug, "x"},
		{"pino debug", `{"level":20,"msg":"x"}`, Debug, "x"},
		{"pino info", `{"level":30,"msg":"x"}`, Info, "x"},
		{"pino warn", `{"level":40,"msg":"x"}`, Warn, "x"},
		{"pino error", `{"level":50,"msg":"x"}`, Error, "x"},
		{"pino fatal", `{"level":60,"msg":"x"}`, Error, "x"},
		{"level wins over later keys", `{"severity":"error","level":"info","msg":"x"}`, Info, "x"},
		{"unknown level word falls to the next key", `{"level":"wat","severity":"error","msg":"x"}`, Error, "x"},
		{"no level at all is info", `{"msg":"x"}`, Info, "x"},
		{"no message keeps the whole line", `{"level":"error","code":5}`, Error, `{"level":"error","code":5}`},
		{"msg wins over message", `{"message":"b","msg":"a","level":"info"}`, Info, "a"},
		{"a message that says error does not matter", `{"level":"info","msg":"error budget is fine"}`, Info, "error budget is fine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, msg, isJSON := LevelOf(tc.line, Warn)
			if !isJSON {
				t.Fatal("isJSON = false")
			}
			if got != tc.want || msg != tc.msg {
				t.Errorf("LevelOf = %q, %q, want %q, %q", got, msg, tc.want, tc.msg)
			}
		})
	}

	for _, line := range []string{`{not json`, `{"level":"error"`, `{}x`, `[1,2]`} {
		if _, _, isJSON := LevelOf(line, ""); isJSON {
			t.Errorf("LevelOf(%q) isJSON = true", line)
		}
	}
	// An empty object is JSON with nothing to say.
	if got, msg, isJSON := LevelOf(`{}`, Warn); !isJSON || got != Info || msg != `{}` {
		t.Errorf("LevelOf({}) = %q, %q, %v", got, msg, isJSON)
	}
}

func TestLevelOfJSONTooLong(t *testing.T) {
	long := `{"level":"error","msg":"` + repeat("x", maxJSONLine) + `"}`
	if _, _, isJSON := LevelOf(long, ""); isJSON {
		t.Error("a line over 64 KiB was read as JSON")
	}
}

func repeat(s string, n int) string {
	b := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		b = append(b, s...)
	}
	return string(b)
}

func TestLevelOfContinuation(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		prev Level
		want Level
	}{
		{"java frame", "\tat com.example.Foo.bar(Foo.java:42)", Error, Error},
		{"at without indent", "at com.example.Foo.bar(Foo.java:42)", Error, Error},
		{"caused by", "Caused by: java.io.IOException: closed", Error, Error},
		{"more", "... 12 more", Error, Error},
		{"indented with spaces", "    File \"/app/x.py\", line 3, in foo", Error, Error},
		{"indented under a warning", "  see the docs", Warn, Warn},
		{"postgres detail", "DETAIL:  Key (id)=(1) already exists.", Error, Error},
		{"postgres hint", "HINT:  Use a different id.", Warn, Warn},
		{"postgres context", "CONTEXT:  SQL statement", Error, Error},
		{"postgres statement", "STATEMENT:  INSERT INTO t VALUES (1)", Error, Error},
		{"postgres detail after the cut time", "[1] DETAIL:  Key (id)=(1) already exists.", Error, Error},
		{"nothing before it is info", "\tat x.y(z)", "", Info},
		{"an ordinary line is not a continuation", "hello world", Error, Info},
		{"... without a number", "... and so on", Error, Info},
		{"detail in a sentence", "the DETAIL: is here", Error, Info},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _, _ := LevelOf(tc.line, tc.prev); got != tc.want {
				t.Errorf("LevelOf(%q, %q) = %q, want %q", tc.line, tc.prev, got, tc.want)
			}
		})
	}
}

// TestParsePostgresDetail goes through Parse, where the time is cut first.
func TestParsePostgresDetail(t *testing.T) {
	a := Parse(`2026-10-24 23:48:36.479 UTC [7] ERROR:  duplicate key value`, Stderr, LineID{}, wide, nil)
	b := Parse(`2026-10-24 23:48:36.479 UTC [7] DETAIL:  Key (id)=(1) already exists.`, Stderr, LineID{}, wide, &a)
	if a.Level != Error || b.Level != Error {
		t.Errorf("levels = %q, %q, want error, error", a.Level, b.Level)
	}
}

func TestParseLevelAndStream(t *testing.T) {
	for in, want := range map[string]Level{"error": Error, "WARN": Warn, " Info ": Info, "debug": Debug} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "fatal", "warning", "all"} {
		if _, err := ParseLevel(in); err == nil {
			t.Errorf("ParseLevel(%q) accepted", in)
		}
	}
	if s, err := ParseStream("stderr"); err != nil || s != Stderr {
		t.Errorf("ParseStream(stderr) = %q, %v", s, err)
	}
	if _, err := ParseStream("both"); err == nil {
		t.Error(`ParseStream("both") accepted`)
	}
}
