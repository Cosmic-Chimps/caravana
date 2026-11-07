package service

import "testing"

func TestRollbackCause(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Caravana 0.17.0 didn't answer its health check within 60 s; 0.16.0 runs again, with its data as it was", "Caravana 0.17.0 didn't answer its health check within 60 s."},
		{"The installer stopped with exit code 3.", "The installer stopped with exit code 3."},
		{"  ", ""},
		{"; only the rest", ""},
	} {
		if got := RollbackCause(tc.in); got != tc.want {
			t.Errorf("RollbackCause(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
