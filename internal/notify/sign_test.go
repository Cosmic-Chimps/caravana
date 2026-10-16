package notify

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	const secret = "whsec_test"
	body := []byte(`{"type":"test"}`)
	now := time.Unix(1_800_000_000, 0)
	good := Sign(secret, now.Unix(), body)

	if !strings.HasPrefix(good, "t=1800000000,v1=") {
		t.Fatalf("Sign = %q, want the t=,v1= form", good)
	}

	tests := []struct {
		name   string
		secret string
		header string
		body   []byte
		now    time.Time
		want   error
	}{
		{"round trip", secret, good, body, now, nil},
		{"inside the tolerance, late", secret, good, body, now.Add(4 * time.Minute), nil},
		{"inside the tolerance, early", secret, good, body, now.Add(-4 * time.Minute), nil},
		{"too old", secret, good, body, now.Add(6 * time.Minute), ErrSignatureTooOld},
		{"from the future", secret, good, body, now.Add(-6 * time.Minute), ErrSignatureTooOld},
		{"tampered body", secret, good, []byte(`{"type":"tset"}`), now, ErrSignatureMismatch},
		{"wrong secret", "other", good, body, now, ErrSignatureMismatch},
		{"tampered time", secret, strings.Replace(good, "t=1800000000", "t=1800000001", 1), body, now, ErrSignatureMismatch},
		{"second v1 matches (rotation)", secret, "t=1800000000,v1=00," + good[strings.Index(good, "v1="):], body, now, nil},
		{"unknown key ignored", secret, good + ",v2=zz", body, now, nil},
		{"empty header", secret, "", body, now, ErrBadSignatureHeader},
		{"no v1", secret, "t=1800000000", body, now, ErrBadSignatureHeader},
		{"no t", secret, good[strings.Index(good, "v1="):], body, now, ErrBadSignatureHeader},
		{"bad t", secret, "t=soon,v1=00", body, now, ErrBadSignatureHeader},
		{"bad hex", secret, "t=1800000000,v1=zz", body, now, ErrBadSignatureHeader},
		{"no equals sign", secret, "garbage", body, now, ErrBadSignatureHeader},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify(tc.secret, tc.header, tc.body, tc.now, 5*time.Minute)
			if !errors.Is(err, tc.want) {
				t.Errorf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}
