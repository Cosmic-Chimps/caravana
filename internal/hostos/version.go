package hostos

// KernelNewer says whether kernel release a is newer than b, comparing the
// numbers inside them as numbers ("6.8.0-149-generic" is newer than
// "6.8.0-9-generic", which plain text order gets wrong) and the words between
// them as text. Equal releases are not newer.
func KernelNewer(a, b string) bool { return compareRelease(a, b) > 0 }

// compareRelease splits both into runs of digits and runs of anything else and
// compares them pairwise: two numbers by value, otherwise as text. A release
// that is a prefix of the other is the older one.
func compareRelease(a, b string) int {
	for a != "" && b != "" {
		var x, y string
		x, a = nextRun(a)
		y, b = nextRun(b)
		if isDigit(x[0]) && isDigit(y[0]) {
			if c := compareNumbers(x, y); c != 0 {
				return c
			}
			continue
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case a != "":
		return 1
	case b != "":
		return -1
	}
	return 0
}

// nextRun cuts the leading run of digits, or of non-digits, off s.
func nextRun(s string) (run, rest string) {
	digits := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == digits {
		i++
	}
	return s[:i], s[i:]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// compareNumbers compares two runs of digits by value without parsing them, so
// a long one can't overflow: leading zeros are dropped, then the longer is the
// larger, then the text decides.
func compareNumbers(x, y string) int {
	for len(x) > 1 && x[0] == '0' {
		x = x[1:]
	}
	for len(y) > 1 && y[0] == '0' {
		y = y[1:]
	}
	switch {
	case len(x) != len(y):
		if len(x) < len(y) {
			return -1
		}
		return 1
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}
