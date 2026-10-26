package metrics

import (
	"math"
	"testing"
)

func mustLayout(t testing.TB, s string) Layout {
	t.Helper()
	l, err := ParseLayout(s)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// bucketsOf puts a count in the bucket that holds sec, for the layout.
func bucketsOf(l Layout, counts map[float64]int64) Buckets {
	b := make(Buckets, len(l)+1)
	for sec, c := range counts {
		i := len(l)
		for j, bound := range l {
			if sec <= bound {
				i = j
				break
			}
		}
		b[i] += c
	}
	return b.Trim()
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLayoutRoundTrip(t *testing.T) {
	l := mustLayout(t, TraefikBounds)
	if len(l) != 11 || l[0] != 0.005 || l[10] != 10 {
		t.Fatalf("layout = %v", l)
	}
	if l.String() != TraefikBounds {
		t.Fatalf("String() = %q", l.String())
	}
	if !l.Equal(mustLayout(t, TraefikBounds)) || l.Equal(mustLayout(t, "0.1,1")) {
		t.Fatal("Equal is wrong")
	}
	for _, bad := range []string{"", "a", "0.1,0.1", "1,0.5", "-1", "0", "+Inf", "0.1,"} {
		if _, err := ParseLayout(bad); err == nil {
			t.Errorf("ParseLayout(%q) accepted", bad)
		}
	}
}

func TestBucketsText(t *testing.T) {
	l := mustLayout(t, TraefikBounds)
	tests := []struct {
		in   string
		want Buckets
		out  string
	}{
		{"", nil, ""},
		{"0,0,0", nil, ""},
		{"3,0,12", Buckets{3, 0, 12}, "3,0,12"},
		{"3,0,12,0,0", Buckets{3, 0, 12}, "3,0,12"},
		{"1,1,1,1,1,1,1,1,1,1,1,5", Buckets{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 5}, "1,1,1,1,1,1,1,1,1,1,1,5"},
	}
	for _, tt := range tests {
		got, err := ParseBuckets(tt.in, l)
		if err != nil {
			t.Fatalf("%q: %v", tt.in, err)
		}
		if got.String() != tt.out || got.Total() != tt.want.Total() {
			t.Errorf("%q: got %v (%q), want %q", tt.in, got, got.String(), tt.out)
		}
	}
	for _, bad := range []string{"1,x", "-1", "1.5", "1,1,1,1,1,1,1,1,1,1,1,1,1"} {
		if _, err := ParseBuckets(bad, l); err == nil {
			t.Errorf("ParseBuckets(%q) accepted", bad)
		}
	}
}

func TestFromCumulative(t *testing.T) {
	tests := []struct {
		name string
		cum  []float64
		want string
	}{
		{"plain", []float64{1, 3, 6, 6}, "1,2,3"},
		{"a bucket read below the one before is lifted (running maximum)", []float64{5, 4, 9, 9}, "5,0,4"},
		{"empty", []float64{0, 0, 0}, ""},
		{"rounded", []float64{0.4, 2.6, 3}, "0,3"},
		{"+Inf above the last bound", []float64{2, 2, 2, 5}, "2,0,0,3"},
	}
	for _, tt := range tests {
		if got := FromCumulative(tt.cum).String(); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestMerge(t *testing.T) {
	l := mustLayout(t, TraefikBounds)
	other := mustLayout(t, "0.1,1")
	a := Histogram{l, Buckets{1, 2}}
	b := Histogram{l, Buckets{0, 1, 4}}
	c := Histogram{other, Buckets{9, 9, 9}}
	d := Histogram{l, nil}
	got, skipped := Merge(l, a, b, c, d)
	if got.String() != "1,3,4" || skipped != 1 {
		t.Fatalf("Merge = %q skipped %d", got.String(), skipped)
	}
	if got, skipped := Merge(l); got != nil || skipped != 0 {
		t.Fatalf("Merge of nothing = %v, %d", got, skipped)
	}
	long := Histogram{l, make(Buckets, 40)}
	if _, skipped := Merge(l, long); skipped != 1 {
		t.Fatal("a row with too many counts must be skipped")
	}
}

// R2's table: the finer buckets against each p95 truly there.
func TestQuantileTableOfResearchR2(t *testing.T) {
	l := mustLayout(t, TraefikBounds)
	tests := []struct {
		name     string
		sec      float64 // every request takes this long
		lo, hi   float64 // the bucket the answer must fall in
		exactMs  float64 // interpolation, when the bucket is full
		wantOver bool
	}{
		{"2 ms (whoami)", 0.002, 0, 0.005, 4.75, false},
		{"40 ms", 0.04, 0.025, 0.05, 25 + 0.95*25, false},
		{"180 ms", 0.18, 0.1, 0.25, 100 + 0.95*150, false},
		{"820 ms", 0.82, 0.5, 1, 500 + 0.95*500, false},
		{"3 s", 3, 2.5, 5, 2500 + 0.95*2500, false},
		{"20 s is over", 20, 10, 10, 10000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := bucketsOf(l, map[float64]int64{tt.sec: 100})
			a, ok := Quantile(l, b, 0.95)
			if !ok {
				t.Fatal("no answer")
			}
			if a.Over != tt.wantOver {
				t.Fatalf("Over = %v", a.Over)
			}
			if a.Seconds < tt.lo || a.Seconds > tt.hi {
				t.Fatalf("p95 = %v, want within [%v, %v]", a.Seconds, tt.lo, tt.hi)
			}
			if !near(a.Seconds*1000, tt.exactMs) {
				t.Fatalf("p95 = %v ms, want %v ms", a.Seconds*1000, tt.exactMs)
			}
		})
	}
}

func TestQuantile(t *testing.T) {
	l := mustLayout(t, "0.1,0.5,1")
	tests := []struct {
		name string
		b    Buckets
		q    float64
		want float64
		over bool
		ok   bool
	}{
		{"empty", nil, 0.95, 0, false, false},
		{"all zero", Buckets{0, 0, 0, 0}, 0.95, 0, false, false},
		{"one request is the slowest one", Buckets{1}, 0.95, 0.095, false, true},
		{"nine fast one slow: rank 9.5 reaches the slow bucket", Buckets{9, 0, 1}, 0.95, 0.5 + 0.5*0.5, false, true},
		{"19 fast one slow: rank 19 is the last fast", Buckets{19, 0, 1}, 0.95, 0.1, false, true},
		{"median in the second bucket", Buckets{2, 6, 2}, 0.5, 0.1 + 0.4*(5.0-2.0)/6.0, false, true},
		{"rank in +Inf", Buckets{1, 0, 0, 9}, 0.95, 1, true, true},
		{"q = 1 in a finite bucket", Buckets{0, 4}, 1, 0.5, false, true},
		{"q = 0", Buckets{0, 4}, 0, 0.1, false, true},
		{"q above 1", Buckets{1}, 1.5, 0, false, false},
		{"q below 0", Buckets{1}, -0.1, 0, false, false},
		{"more counts than the layout has", Buckets{1, 1, 1, 1, 1}, 0.5, 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, ok := Quantile(l, tt.b, tt.q)
			if ok != tt.ok {
				t.Fatalf("ok = %v", ok)
			}
			if !ok {
				return
			}
			if a.Over != tt.over || !near(a.Seconds, tt.want) {
				t.Fatalf("got %+v, want %v over=%v", a, tt.want, tt.over)
			}
		})
	}
	if _, ok := Quantile(nil, Buckets{1}, 0.5); ok {
		t.Fatal("an empty layout has no answer")
	}
}

// The merge of two services is the quantile of the summed buckets, which is
// not the mean of the two p95s.
func TestMergedQuantileIsNotAMeanOfQuantiles(t *testing.T) {
	l := mustLayout(t, TraefikBounds)
	fast := Histogram{l, bucketsOf(l, map[float64]int64{0.002: 990})}
	slow := Histogram{l, bucketsOf(l, map[float64]int64{3: 10})}
	merged, _ := Merge(l, fast, slow)
	got, _ := Quantile(l, merged, 0.95)
	pa, _ := Quantile(l, fast.Buckets, 0.95)
	pb, _ := Quantile(l, slow.Buckets, 0.95)
	if mean := (pa.Seconds + pb.Seconds) / 2; near(got.Seconds, mean) {
		t.Fatalf("merged p95 equals the mean of p95s (%v)", mean)
	}
	if got.Seconds > 0.005 {
		t.Fatalf("p95 = %v: 99%% of requests took 2 ms", got.Seconds)
	}
}
