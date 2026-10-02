package api

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"time"
)

// Every body the API reads or writes goes through encoding/json/v2 with these
// options (research R5). The store keeps encoding/json v1 and its integer
// nanoseconds; only the API speaks duration strings.
var (
	decodeOptions = json.JoinOptions(
		json.RejectUnknownMembers(true),
		json.WithUnmarshalers(json.UnmarshalFromFunc(unmarshalDuration)),
	)
	encodeOptions = json.JoinOptions(
		json.Deterministic(true),
		json.WithMarshalers(json.MarshalToFunc(marshalDuration)),
	)
)

// durationError is what unmarshalDuration returns. json/v2 wraps it in a
// *json.SemanticError that carries the field's path, and decodeJSON shows the
// client our message instead of a generic "wrong type".
type durationError struct{ msg string }

func (e *durationError) Error() string { return e.msg }

// unmarshalDuration reads a time.Duration from a duration string ("10s",
// "1m30s"). Numbers are refused: "10" could mean seconds or nanoseconds.
func unmarshalDuration(dec *jsontext.Decoder, d *time.Duration) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '"' {
		return &durationError{`must be a duration string such as "10s"`}
	}
	v, err := time.ParseDuration(tok.String())
	if err != nil {
		return &durationError{fmt.Sprintf("%q is not a duration; use a string such as \"10s\"", tok.String())}
	}
	*d = v
	return nil
}

// marshalDuration writes a time.Duration as a duration string. Zero becomes
// "", which omitempty leaves out, so an unset interval reads back unset.
func marshalDuration(enc *jsontext.Encoder, d time.Duration) error {
	if d == 0 {
		return enc.WriteToken(jsontext.String(""))
	}
	return enc.WriteToken(jsontext.String(d.String()))
}

// encodeJSON writes v with the API's options, followed by a newline like
// encoding/json's Encoder.
func encodeJSON(w io.Writer, v any) error {
	if err := json.MarshalWrite(w, v, encodeOptions); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
