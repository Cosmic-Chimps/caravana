package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// callTimeout is how long one API call may take, from connecting to the end of
// the answer. wait_for_deployment makes several calls and has its own limit.
const callTimeout = 30 * time.Second

// maxAnswerBytes bounds what one API answer may hold. The largest real answer
// is a list of 500 log lines; this is far above it and keeps a misbehaving
// address from filling memory.
const maxAnswerBytes = 16 << 20

// operation is one API operation: its ID in api/openapi.yaml, its method and
// its path with {name} placeholders. A test checks each one against the
// contract.
type operation struct {
	ID     string
	Method string
	Path   string
}

// expand fills the path's placeholders from pairs of name and value. Values are
// path-escaped, so an ID can never add a path segment.
func (o operation) expand(pairs ...string) string {
	p := o.Path
	for i := 0; i+1 < len(pairs); i += 2 {
		p = strings.ReplaceAll(p, "{"+pairs[i]+"}", url.PathEscape(pairs[i+1]))
	}
	return p
}

// call is one request to the API.
type call struct {
	op     operation
	params []string   // name, value, name, value: the path's placeholders
	query  url.Values // may be nil
	body   any        // marshalled as JSON; nil for none
	raw    []byte     // a body already in JSON; used instead of body when set
	header map[string]string
}

// reply is what a successful (2xx) call answered.
type reply struct {
	status int
	body   []byte
	header http.Header
}

// toolError is a failed tool call. Text is what the agent reads: it is the
// whole message, in Caravana's words, and never holds the token. Code is a
// short name for the log (the API's error code, or "unreachable", "redirect",
// "invalid_input" and so on), and Status the HTTP status when there was one.
type toolError struct {
	Text   string
	Code   string
	Status int
}

func (e *toolError) Error() string { return e.Text }

// apiClient calls Caravana's API with the owner's token.
type apiClient struct {
	baseURL   string
	token     string
	userAgent string
	http      *http.Client
	timeout   time.Duration
}

// newAPIClient makes the client. It works on a copy of hc (the default client
// when nil) with redirects switched off: a redirect would send the token
// wherever the answer says, so a 3xx is reported instead.
func newAPIClient(cfg Config, hc *http.Client) *apiClient {
	if hc == nil {
		hc = &http.Client{}
	}
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	version := cfg.Version
	if version == "" {
		version = "dev"
	}
	return &apiClient{
		baseURL:   cfg.URL,
		token:     cfg.Token,
		userAgent: "caravana-mcp/" + version,
		http:      &c,
		timeout:   callTimeout,
	}
}

// redact replaces the token in s. The token is never meant to be in a
// message; this is the last line of defence if an address or a server ever
// echoes it back.
func (c *apiClient) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[redacted]")
}

// do makes the call. A 2xx answer is returned; everything else is a
// *toolError that says what happened.
func (c *apiClient) do(ctx context.Context, cl call) (*reply, error) {
	var body io.Reader
	switch {
	case cl.raw != nil:
		body = bytes.NewReader(cl.raw)
	case cl.body != nil:
		data, err := json.Marshal(cl.body)
		if err != nil {
			return nil, fmt.Errorf("encode the %s request: %w", cl.op.ID, err)
		}
		body = bytes.NewReader(data)
	}

	target := c.baseURL + cl.op.expand(cl.params...)
	if len(cl.query) > 0 {
		target += "?" + cl.query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, cl.op.Method, target, body)
	if err != nil {
		return nil, fmt.Errorf("build the %s request: %w", cl.op.ID, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range cl.header {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.unreachable(ctx, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes+1))
	if err != nil {
		return nil, c.unreachable(ctx, err)
	}
	if len(data) > maxAnswerBytes {
		return nil, &toolError{Code: "too_large", Status: resp.StatusCode,
			Text: fmt.Sprintf("Caravana's answer to %s is larger than %d MiB; not read", cl.op.ID, maxAnswerBytes>>20)}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &reply{status: resp.StatusCode, body: data, header: resp.Header}, nil
	}
	return nil, c.failure(cl.op, resp.StatusCode, data)
}

// unreachable is the error for a call that got no answer: the address is
// wrong, the host is down, TLS failed, or the call timed out.
func (c *apiClient) unreachable(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &toolError{Code: "unreachable",
			Text: fmt.Sprintf("couldn't reach Caravana at %s: no answer within %s", c.baseURL, c.timeout)}
	case errors.Is(ctx.Err(), context.Canceled):
		// The agent's client gave up on the call; it will not read this.
		return ctx.Err()
	}
	// *url.Error quotes the method and the whole address; keep what went wrong.
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	return &toolError{Code: "unreachable",
		Text: c.redact(fmt.Sprintf("couldn't reach Caravana at %s: %v", c.baseURL, err))}
}

// apiErrorBody is the API's error: {"error": {"code", "message", "field"}}.
type apiErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Field   string `json:"field"`
	} `json:"error"`
}

// isUnknownRoute says whether e is the answer Caravana gives to a path it has
// no route for, as opposed to "no such app" and the like.
func isUnknownRoute(e apiErrorBody) bool {
	return e.Error.Code == "not_found" && e.Error.Message == "no such endpoint"
}

// failure turns a non-2xx answer into the message the agent reads. The API's
// own refusals keep their code, message and field; the answers that have no
// JSON error (an older Caravana, a proxy in between) are described by status.
func (c *apiClient) failure(op operation, status int, data []byte) *toolError {
	var e apiErrorBody
	hasError := json.Unmarshal(data, &e) == nil && e.Error.Code != ""

	switch {
	case status >= 300 && status < 400:
		return &toolError{Code: "redirect", Status: status, Text: fmt.Sprintf(
			"Caravana answered with a redirect (HTTP %d) and it was not followed, so the token is not sent elsewhere: check CARAVANA_URL", status)}
	case status == http.StatusUnauthorized:
		return &toolError{Code: "unauthorized", Status: status, Text: "Caravana refused the token (401 unauthorized): " +
			"check CARAVANA_TOKEN, or make a new token in Settings › API tokens"}
	case status == http.StatusNotFound && (!hasError || isUnknownRoute(e)):
		// A route an older Caravana lacks. Caravana itself answers those with
		// its JSON "no such endpoint"; a proxy in between may answer without
		// a JSON error at all.
		return &toolError{Code: "not_offered", Status: status, Text: fmt.Sprintf(
			"this Caravana doesn't offer %s; it is older than this binary", op.ID)}
	case hasError:
		text := fmt.Sprintf("Caravana refused: %s: %s", e.Error.Code, e.Error.Message)
		if e.Error.Field != "" {
			text += fmt.Sprintf(" (field %s)", e.Error.Field)
		}
		text += fmt.Sprintf(" (HTTP %d)", status)
		return &toolError{Code: e.Error.Code, Status: status, Text: c.redact(text)}
	}
	return &toolError{Code: "http_error", Status: status, Text: fmt.Sprintf(
		"Caravana answered HTTP %d to %s without an error message", status, op.ID)}
}
