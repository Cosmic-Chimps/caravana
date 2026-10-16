package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SignatureHeader is the header a webhook message carries its signature in.
const SignatureHeader = "Caravana-Signature"

// Sign returns the value of the Caravana-Signature header for a message sent
// at Unix time t: "t=<t>,v1=<hex>", where v1 is the HMAC-SHA256 of
// "<t>.<body>" under secret. Putting the time under the signature lets a
// receiver refuse a message that was captured and replayed later.
func Sign(secret string, t int64, body []byte) string {
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac(secret, t, body)))
}

// mac is the raw HMAC that Sign encodes and Verify compares.
func mac(secret string, t int64, body []byte) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(strconv.FormatInt(t, 10)))
	h.Write([]byte("."))
	h.Write(body)
	return h.Sum(nil)
}

// The ways Verify refuses a message.
var (
	ErrBadSignatureHeader = errors.New("the signature header is malformed")
	ErrSignatureMismatch  = errors.New("the signature does not match")
	ErrSignatureTooOld    = errors.New("the signature is outside the allowed time window")
)

// Verify checks the Caravana-Signature header of a message with the channel's
// signing secret, the way a receiver should. It refuses a header it cannot
// read, a timestamp more than tolerance away from now (in either direction),
// and a signature that does not match the raw body. Caravana itself only calls
// it in tests; the shape is documented in the API for receivers to copy.
func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	var t int64
	var haveT bool
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return ErrBadSignatureHeader
		}
		switch key {
		case "t":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return ErrBadSignatureHeader
			}
			t, haveT = n, true
		case "v1":
			sig, err := hex.DecodeString(value)
			if err != nil {
				return ErrBadSignatureHeader
			}
			sigs = append(sigs, sig)
		}
		// Other keys are ignored so a later version can add some.
	}
	if !haveT || len(sigs) == 0 {
		return ErrBadSignatureHeader
	}

	age := now.Sub(time.Unix(t, 0))
	if age < 0 {
		age = -age
	}
	if age > tolerance {
		return ErrSignatureTooOld
	}

	want := mac(secret, t, body)
	for _, sig := range sigs {
		// hmac.Equal compares in constant time, so timing does not leak how
		// much of a guess was right.
		if hmac.Equal(sig, want) {
			return nil
		}
	}
	return ErrSignatureMismatch
}
