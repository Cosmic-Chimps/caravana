package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Errors callers can check with errors.Is. Returned errors wrap one of them
// and name the registry and the image.
var (
	// ErrNotFound: the registry has no such image or tag, or it's private.
	// Without credentials the two can't be told apart.
	ErrNotFound = errors.New("image not found")
	// ErrUnavailable: the registry couldn't be used right now (network
	// error, 5xx, rate limit, or an answer we can't read).
	ErrUnavailable = errors.New("registry unavailable")
)

// defaultEndpoints are the registries' real API addresses.
var defaultEndpoints = map[string]string{
	"docker.io": "https://registry-1.docker.io",
	"ghcr.io":   "https://ghcr.io",
}

// acceptManifests lists the index types first, so a multi-architecture tag
// answers with its index and the pinned digest covers every architecture.
var acceptManifests = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// maxManifestBytes bounds a manifest body read to compute its digest.
const maxManifestBytes = 4 << 20

// Client resolves tags to digests. Its zero value is ready to use.
type Client struct {
	// HTTP is the client for registry requests; nil means a default one.
	HTTP *http.Client
	// Endpoints maps a registry ("docker.io", "ghcr.io") to its base URL;
	// nil means the real ones. Tests point it at a fake.
	Endpoints map[string]string

	retryDelays []time.Duration // nil means 0.5 s, then 1 s
}

// Resolve returns ref pinned by digest ("<name>@sha256:…"). A reference that
// already has a digest comes back without any request. The caller's ctx
// bounds the whole resolution.
func (c *Client) Resolve(ctx context.Context, ref Reference) (string, error) {
	if ref.Digest != "" {
		return ref.Pinned(ref.Digest), nil
	}
	digest, err := c.resolve(ctx, ref, ref.Tag)
	if err != nil {
		return "", fmt.Errorf("%s: %s: %w", ref.Registry, ref, err)
	}
	return ref.Pinned(digest), nil
}

// Exists checks that the registry still has ref's digest: an image pinned
// earlier may have been deleted since. A ref without a digest is an error.
// The answer is nil, or an error wrapping ErrNotFound or ErrUnavailable.
func (c *Client) Exists(ctx context.Context, ref Reference) error {
	if ref.Digest == "" {
		return fmt.Errorf("%s: %s: no digest to check", ref.Registry, ref)
	}
	if _, err := c.resolve(ctx, ref, ref.Digest); err != nil {
		return fmt.Errorf("%s: %s: %w", ref.Registry, ref, err)
	}
	return nil
}

// resolve asks for the digest of the manifest named by reference (a tag or a
// digest), retrying network errors and 5xx.
func (c *Client) resolve(ctx context.Context, ref Reference, reference string) (string, error) {
	delays := c.retryDelays
	if delays == nil {
		delays = []time.Duration{500 * time.Millisecond, time.Second}
	}
	base := defaultEndpoints[ref.Registry]
	if e, ok := c.Endpoints[ref.Registry]; ok {
		base = e
	}
	manifestURL := base + "/v2/" + ref.Repository + "/manifests/" + url.PathEscape(reference)

	for attempt := 0; ; attempt++ {
		digest, retry, err := c.try(ctx, ref, manifestURL)
		if err == nil || !retry || attempt >= len(delays) {
			return digest, err
		}
		select {
		case <-time.After(delays[attempt]):
		case <-ctx.Done():
			return "", err
		}
	}
}

// errNotFoundOrPrivate is the one message for "missing or private".
var errNotFoundOrPrivate = fmt.Errorf("%w, or private (Caravana doesn't support registry credentials yet)", ErrNotFound)

// try makes one attempt: HEAD the manifest, do the token handshake if the
// registry asks for it, and read the digest. retry says whether another
// attempt could help.
func (c *Client) try(ctx context.Context, ref Reference, manifestURL string) (digest string, retry bool, err error) {
	resp, err := c.request(ctx, http.MethodHead, manifestURL, "")
	if err != nil {
		return "", true, err
	}
	resp.Body.Close()

	token := ""
	if resp.StatusCode == http.StatusUnauthorized {
		ch, ok := parseChallenge(resp.Header.Get("WWW-Authenticate"))
		if !ok {
			return "", false, errNotFoundOrPrivate
		}
		if token, retry, err = c.token(ctx, ch, ref.Repository); err != nil {
			return "", retry, err
		}
		if resp, err = c.request(ctx, http.MethodHead, manifestURL, token); err != nil {
			return "", true, err
		}
		resp.Body.Close()
	}

	switch code := resp.StatusCode; {
	case code == http.StatusOK:
	case code == http.StatusUnauthorized, code == http.StatusForbidden, code == http.StatusNotFound:
		return "", false, errNotFoundOrPrivate
	case code == http.StatusTooManyRequests:
		return "", false, fmt.Errorf("%w: it is rate-limiting requests (429); try again later", ErrUnavailable)
	case code >= 500:
		return "", true, fmt.Errorf("%w: it answered %d", ErrUnavailable, code)
	default:
		return "", false, fmt.Errorf("%w: unexpected answer %d", ErrUnavailable, code)
	}

	digest = resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		// Some registries leave the header out of HEAD answers; the digest
		// is then the SHA-256 of the manifest itself.
		if digest, err = c.hashManifest(ctx, manifestURL, token); err != nil {
			return "", true, err
		}
	}
	if !digestRE.MatchString(digest) {
		return "", false, fmt.Errorf("%w: it answered with a digest we can't pin (%q)", ErrUnavailable, digest)
	}
	return digest, false, nil
}

func (c *Client) hashManifest(ctx context.Context, manifestURL, token string) (string, error) {
	resp, err := c.request(ctx, http.MethodGet, manifestURL, token)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: reading the manifest answered %d", ErrUnavailable, resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(resp.Body, maxManifestBytes)); err != nil {
		return "", fmt.Errorf("%w: reading the manifest: %v", ErrUnavailable, err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// request sends one request. Network errors (and an expired ctx) come back
// as ErrUnavailable.
func (c *Client) request(ctx context.Context, method, target, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Accept", acceptManifests)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// The error names the URL (no secrets in it) and the cause.
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return resp, nil
}

// challenge is a parsed `WWW-Authenticate: Bearer …` header.
type challenge struct{ realm, service string }

// parseChallenge reads Bearer realm="…",service="…",scope="…".
func parseChallenge(header string) (challenge, bool) {
	scheme, params, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return challenge{}, false
	}
	var ch challenge
	for params != "" {
		params = strings.TrimLeft(params, " ,")
		key, rest, ok := strings.Cut(params, "=")
		if !ok {
			break
		}
		var value string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				return challenge{}, false
			}
			value, params = rest[1:end+1], rest[end+2:]
		} else {
			value, params, _ = strings.Cut(rest, ",")
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "realm":
			ch.realm = value
		case "service":
			ch.service = value
		}
	}
	return ch, ch.realm != ""
}

// token gets an anonymous pull token for repo. The token is never logged or
// put in an error.
func (c *Client) token(ctx context.Context, ch challenge, repo string) (token string, retry bool, err error) {
	u, err := url.Parse(ch.realm)
	if err != nil {
		return "", false, fmt.Errorf("%w: bad token realm %q", ErrUnavailable, ch.realm)
	}
	q := u.Query()
	if ch.service != "" {
		q.Set("service", ch.service)
	}
	q.Set("scope", "repository:"+repo+":pull")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("%w: token request: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	switch code := resp.StatusCode; {
	case code == http.StatusOK:
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return "", false, errNotFoundOrPrivate
	case code == http.StatusTooManyRequests:
		return "", false, fmt.Errorf("%w: it is rate-limiting token requests (429); try again later", ErrUnavailable)
	case code >= 500:
		return "", true, fmt.Errorf("%w: token request answered %d", ErrUnavailable, code)
	default:
		return "", false, fmt.Errorf("%w: token request answered %d", ErrUnavailable, code)
	}

	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", false, fmt.Errorf("%w: unreadable token answer", ErrUnavailable)
	}
	token = body.Token
	if token == "" {
		token = body.AccessToken
	}
	if token == "" {
		return "", false, fmt.Errorf("%w: token answer has no token", ErrUnavailable)
	}
	return token, false, nil
}
