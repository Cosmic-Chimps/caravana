package api

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// maxBodyBytes is the largest request body accepted.
const maxBodyBytes = 1 << 20 // 1 MiB

// decodeJSON reads r's JSON body into v. Every endpoint with a body uses it,
// so they all accept and reject the same things:
//   - Content-Type must be application/json, with or without parameters; a
//     charset, if given, must be UTF-8.
//   - Fields v doesn't define are rejected, naming the field's full path.
//   - Empty, malformed or oversized bodies, and data after the JSON value,
//     are rejected.
//
// It uses encoding/json/v2, whose errors carry the full path of the field at
// fault (e.g. /services/0/ports/0/bogus), with the options in json.go:
// durations are strings such as "10s".
func decodeJSON(r *http.Request, v any) error {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &Error{Status: http.StatusUnsupportedMediaType, Code: codeUnsupportedMediaType,
			Message: "request body must be application/json"}
	}
	if cs, ok := params["charset"]; ok && !strings.EqualFold(cs, "utf-8") {
		return &Error{Status: http.StatusUnsupportedMediaType, Code: codeUnsupportedMediaType,
			Message: fmt.Sprintf("charset %q is not supported; use UTF-8", cs)}
	}

	// Read one byte more than the limit, to tell "exactly the limit" from
	// "too big".
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return &Error{Status: http.StatusBadRequest, Code: codeBadRequest, Message: "could not read the request body"}
	}
	if len(body) > maxBodyBytes {
		return &Error{Status: http.StatusRequestEntityTooLarge, Code: codeBodyTooLarge,
			Message: fmt.Sprintf("request body is larger than %d bytes", maxBodyBytes)}
	}
	if len(body) == 0 {
		return &Error{Status: http.StatusBadRequest, Code: codeBadRequest, Message: "request body is required"}
	}

	err = json.Unmarshal(body, v, decodeOptions)
	if err == nil {
		return nil
	}
	var se *json.SemanticError
	var de *durationError
	switch {
	case errors.As(err, &se) && errors.As(err, &de):
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: pointerToField(se.JSONPointer),
			Message: de.msg}
	case errors.As(err, &se) && errors.Is(err, json.ErrUnknownName):
		field := pointerToField(se.JSONPointer)
		return &Error{Status: http.StatusBadRequest, Code: codeUnknownField, Field: field,
			Message: fmt.Sprintf("%q is not a field of this request", field)}
	case errors.As(err, &se) && se.JSONPointer != "":
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: pointerToField(se.JSONPointer),
			Message: "has the wrong type"}
	default:
		return &Error{Status: http.StatusBadRequest, Code: codeBadRequest, Message: "request body is not valid JSON"}
	}
}

// decodeOptionalJSON is decodeJSON for endpoints whose body may be left out.
// An empty body reports present = false, whatever its Content-Type; any other
// body follows decodeJSON's rules.
func decodeOptionalJSON(r *http.Request, v any) (present bool, err error) {
	var first [1]byte
	n, err := io.ReadFull(r.Body, first[:])
	if n == 0 {
		if err != nil && !errors.Is(err, io.EOF) {
			return false, &Error{Status: http.StatusBadRequest, Code: codeBadRequest, Message: "could not read the request body"}
		}
		return false, nil
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(first[:n]), r.Body), r.Body}
	return true, decodeJSON(r, v)
}

// pointerToField turns a JSON pointer like /services/0/ports/0/name into the
// field path style feature 001 uses: services[0].ports[0].name. A pointer
// can't tell a map key from a field name, so all-digit segments are written
// as list indexes.
func pointerToField(p jsontext.Pointer) string {
	var b strings.Builder
	for tok := range p.Tokens() { // Tokens already unescapes ~0 and ~1
		if isIndex(tok) {
			b.WriteString("[" + tok + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(tok)
	}
	return b.String()
}

func isIndex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
