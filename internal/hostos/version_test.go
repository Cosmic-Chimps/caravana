package hostos

import "testing"

func TestReleaseNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"0.16.0", "0.15.0", true},
		{"v0.16.0", "0.15.0", true}, // a tag has a leading v, a build's version has none
		{"v0.16.0", "v0.15.0", true},
		{"0.15.0", "0.15.0", false},
		{"0.14.0", "0.15.0", false},
		{"0.10.0", "0.9.0", true}, // by number, not by text
		{"0.9.0", "0.10.0", false},
		{"1.0.0", "0.99.99", true},
		{"0.15.1", "0.15.0", true},
		{"0.15.0", "0.15.0-rc.1", false}, // sort -V puts the suffixed one after the plain one
		{"0.16.0-rc.1", "0.15.0", true},
		// A build that isn't a release never compares, on either side.
		{"0.16.0", "dev", false},
		{"0.16.0", "0.0.0-dev.1730000000", false},
		{"0.16.0", "", false},
		{"dev", "0.15.0", false},
		{"0.0.0-dev.1", "0.0.0-dev.2", false},
		{"latest", "0.15.0", false},
		{"0.16", "0.15.0", false}, // not X.Y.Z
		{"0.16.0; rm -rf /", "0.15.0", false},
	} {
		if got := ReleaseNewer(c.latest, c.current); got != c.want {
			t.Errorf("ReleaseNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestIsReleaseVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"0.15.0": true, "v0.15.0": true, "1.2.3-rc.1": true, " 0.15.0 ": true,
		"dev": false, "": false, "0.0.0-dev.1730000000": false, "v0.0.0-dev.1": false, "0.15": false, "x.y.z": false,
	} {
		if got := IsReleaseVersion(v); got != want {
			t.Errorf("IsReleaseVersion(%q) = %v, want %v", v, got, want)
		}
	}
}
