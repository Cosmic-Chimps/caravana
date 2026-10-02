package api

import (
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type decodeTarget struct {
	ID       string `json:"id"`
	Count    int    `json:"count"`
	Services []struct {
		Name  string `json:"name"`
		Ports []struct {
			Name string `json:"name"`
		} `json:"ports"`
	} `json:"services"`
}

func TestDecode(t *testing.T) {
	// A valid body of exactly maxBodyBytes: {"id":"aaa…"}.
	exact := `{"id":"` + strings.Repeat("a", maxBodyBytes-len(`{"id":""}`)) + `"}`

	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int    // 0 means success
		wantCode    string // when failing
		wantField   string
	}{
		{name: "plain media type", contentType: "application/json", body: `{"id":"a"}`},
		{name: "with charset", contentType: "application/json; charset=UTF-8", body: `{"id":"a"}`},
		{name: "case-insensitive", contentType: "Application/JSON; Charset=utf-8", body: `{"id":"a"}`},
		{name: "exactly the size limit", contentType: "application/json", body: exact},

		{name: "text/plain", contentType: "text/plain", body: `{}`, wantStatus: 415, wantCode: codeUnsupportedMediaType},
		{name: "no media type", contentType: "", body: `{}`, wantStatus: 415, wantCode: codeUnsupportedMediaType},
		{name: "other charset", contentType: "application/json; charset=latin1", body: `{}`, wantStatus: 415, wantCode: codeUnsupportedMediaType},

		{name: "unknown top-level field", contentType: "application/json", body: `{"bogus":1}`, wantStatus: 400, wantCode: codeUnknownField, wantField: "bogus"},
		{name: "unknown nested field", contentType: "application/json",
			body:       `{"services":[{"ports":[{"name":"http","bogus":1}]}]}`,
			wantStatus: 400, wantCode: codeUnknownField, wantField: "services[0].ports[0].bogus"},
		{name: "field names are case-sensitive", contentType: "application/json", body: `{"ID":"a"}`, wantStatus: 400, wantCode: codeUnknownField, wantField: "ID"},

		{name: "wrong type", contentType: "application/json", body: `{"count":"x"}`, wantStatus: 400, wantCode: codeInvalidField, wantField: "count"},

		{name: "empty body", contentType: "application/json", body: ``, wantStatus: 400, wantCode: codeBadRequest},
		{name: "truncated", contentType: "application/json", body: `{"id":`, wantStatus: 400, wantCode: codeBadRequest},
		{name: "trailing data", contentType: "application/json", body: `{} {}`, wantStatus: 400, wantCode: codeBadRequest},

		{name: "over the size limit", contentType: "application/json", body: exact + " ", wantStatus: 413, wantCode: codeBodyTooLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(tc.body))
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			var v decodeTarget
			err := decodeJSON(r, &v)

			if tc.wantStatus == 0 {
				if err != nil {
					t.Fatalf("decodeJSON() = %v, want nil", err)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("decodeJSON() = %v, want *Error", err)
			}
			if e.Status != tc.wantStatus || e.Code != tc.wantCode || e.Field != tc.wantField {
				t.Errorf("got status %d code %q field %q; want %d %q %q (message %q)",
					e.Status, e.Code, e.Field, tc.wantStatus, tc.wantCode, tc.wantField, e.Message)
			}
			if e.Message == "" {
				t.Error("empty message")
			}
		})
	}
}

func TestPointerToField(t *testing.T) {
	tests := []struct {
		pointer, want string
	}{
		{"/services/0/ports/0/bogus", "services[0].ports[0].bogus"},
		{"/id", "id"},
		{"/a~1b", "a/b"},
		{"/a~0b", "a~b"},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.pointer, func(t *testing.T) {
			if got := pointerToField(jsontext.Pointer(tc.pointer)); got != tc.want {
				t.Errorf("pointerToField(%q) = %q, want %q", tc.pointer, got, tc.want)
			}
		})
	}
}
