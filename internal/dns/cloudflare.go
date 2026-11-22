package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultCloudflareURL is Cloudflare's v4 API.
const DefaultCloudflareURL = "https://api.cloudflare.com/client/v4"

const (
	callTimeout = 10 * time.Second
	maxBody     = 4 << 20
	pageSize    = 100
	maxPages    = 50 // a zone of 5000 records is far past what Caravana lists
)

// Cloudflare is a Provider over Cloudflare's v4 API, with the standard
// library only. Errors are classified by status; a body is never read into
// an error.
type Cloudflare struct {
	token   string
	baseURL string
	client  *http.Client
}

// CloudflareOption changes a Cloudflare client.
type CloudflareOption func(*Cloudflare)

// WithBaseURL points the client at another address (tests and the lab).
func WithBaseURL(u string) CloudflareOption {
	return func(c *Cloudflare) { c.baseURL = strings.TrimRight(u, "/") }
}

// NewCloudflare returns a client that sends token as a bearer token. Each
// call takes at most 10 seconds and redirects are not followed.
func NewCloudflare(token string, opts ...CloudflareOption) *Cloudflare {
	c := &Cloudflare{
		token:   token,
		baseURL: DefaultCloudflareURL,
		client: &http.Client{
			Timeout: callTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// CloudflareFactory is the Factory for kind "cloudflare". baseURL is "" for
// Cloudflare's own API; the lab gives its fake's address.
func CloudflareFactory(baseURL string) Factory {
	return func(kind, token string) (Provider, error) {
		if kind != "cloudflare" {
			return nil, fmt.Errorf("dns provider %q: %w", kind, ErrRefused)
		}
		if token == "" {
			return nil, fmt.Errorf("dns provider %q: no token: %w", kind, ErrRefused)
		}
		var opts []CloudflareOption
		if baseURL != "" {
			opts = append(opts, WithBaseURL(baseURL))
		}
		return NewCloudflare(token, opts...), nil
	}
}

// cfRecord is Cloudflare's record.
type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment,omitempty"`
	TTL     int    `json:"ttl"`
}

// record converts: cfRecord has Record's fields, with Cloudflare's JSON names.
func (r cfRecord) record() Record { return Record(r) }

// envelope is the part of Cloudflare's answer Caravana reads.
type envelope struct {
	Success    bool            `json:"success"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// do sends one request and returns the decoded envelope. status is the HTTP
// status when there was an answer (0 for none). op names the call in errors.
// A 404 is returned as status 404 with a nil error: each caller says what it
// means.
func (c *Cloudflare) do(ctx context.Context, op, method, path string, body any) (envelope, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return envelope{}, 0, &ProviderError{Op: op, Err: ErrRefused}
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return envelope{}, 0, &ProviderError{Op: op, Err: ErrUnavailable}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		// The error of net/http holds the URL: keep only the class.
		return envelope{}, 0, &ProviderError{Op: op, Err: ErrUnavailable}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return envelope{}, resp.StatusCode, &ProviderError{Op: op, Err: ErrUnavailable}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return envelope{}, resp.StatusCode, nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return envelope{}, resp.StatusCode, &ProviderError{Op: op, Err: ErrRefused}
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500, resp.StatusCode >= 300 && resp.StatusCode < 400:
		return envelope{}, resp.StatusCode, &ProviderError{Op: op, Err: ErrUnavailable}
	case resp.StatusCode >= 400:
		return envelope{}, resp.StatusCode, &ProviderError{Op: op, Err: ErrRefused}
	}
	var env envelope
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &env); err != nil {
			return envelope{}, resp.StatusCode, &ProviderError{Op: op, Err: ErrUnavailable}
		}
		if !env.Success {
			return envelope{}, resp.StatusCode, &ProviderError{Op: op, Err: ErrRefused}
		}
	}
	return env, resp.StatusCode, nil
}

// FindZone looks the zone up by name.
func (c *Cloudflare) FindZone(ctx context.Context, zone string) (string, error) {
	const op = "find zone"
	env, status, err := c.do(ctx, op, http.MethodGet, "/zones?name="+url.QueryEscape(zone), nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", &ProviderError{Op: op, Err: ErrZoneNotFound}
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Result, &zones); err != nil {
		return "", &ProviderError{Op: op, Err: ErrUnavailable}
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, zone) && z.ID != "" {
			return z.ID, nil
		}
	}
	return "", &ProviderError{Op: op, Err: ErrZoneNotFound}
}

// Records lists the records of a zone, with this name when name is not empty.
func (c *Cloudflare) Records(ctx context.Context, zoneID, name string) ([]Record, error) {
	const op = "list records"
	var out []Record
	for page := 1; page <= maxPages; page++ {
		q := url.Values{"per_page": {fmt.Sprint(pageSize)}, "page": {fmt.Sprint(page)}}
		if name != "" {
			q.Set("name", name)
		}
		env, status, err := c.do(ctx, op, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			return nil, &ProviderError{Op: op, Err: ErrZoneNotFound}
		}
		var recs []cfRecord
		if err := json.Unmarshal(env.Result, &recs); err != nil {
			return nil, &ProviderError{Op: op, Err: ErrUnavailable}
		}
		for _, r := range recs {
			out = append(out, r.record())
		}
		if env.ResultInfo.TotalPages <= page || len(recs) == 0 {
			break
		}
	}
	return out, nil
}

// Create makes a record: ttl is automatic unless r says otherwise.
func (c *Cloudflare) Create(ctx context.Context, zoneID string, r Record) (Record, error) {
	const op = "create record"
	ttl := r.TTL
	if ttl == 0 {
		ttl = 1
	}
	body := cfRecord{Type: r.Type, Name: r.Name, Content: r.Content, Proxied: r.Proxied, Comment: r.Comment, TTL: ttl}
	env, status, err := c.do(ctx, op, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", body)
	if err != nil {
		return Record{}, err
	}
	if status == http.StatusNotFound {
		return Record{}, &ProviderError{Op: op, Err: ErrZoneNotFound}
	}
	var made cfRecord
	if err := json.Unmarshal(env.Result, &made); err != nil || made.ID == "" {
		return Record{}, &ProviderError{Op: op, Err: ErrUnavailable}
	}
	return made.record(), nil
}

// Update sets the proxy setting of the record r.ID.
func (c *Cloudflare) Update(ctx context.Context, zoneID string, r Record) error {
	const op = "update record"
	body := struct {
		Proxied bool `json:"proxied"`
	}{r.Proxied}
	_, status, err := c.do(ctx, op, http.MethodPatch, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(r.ID), body)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return &ProviderError{Op: op, Err: ErrRecordNotFound}
	}
	return nil
}

// Delete removes a record; one already gone is fine.
func (c *Cloudflare) Delete(ctx context.Context, zoneID, recordID string) error {
	_, _, err := c.do(ctx, "delete record", http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil)
	return err
}
