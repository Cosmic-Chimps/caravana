package registry

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
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
	// ErrUnauthorized: Caravana sent the credentials it has stored for the
	// registry, and the registry refused them (401 or 403 on the token or the
	// manifest request).
	ErrUnauthorized = errors.New("the registry refused the stored credentials")
	// ErrUnavailable: the registry couldn't be used right now (network
	// error, 5xx, rate limit, or an answer we can't read), or it isn't a
	// registry we can talk to (no HTTPS, not a registry at all).
	ErrUnavailable = errors.New("registry unavailable")

	// errLoginRequired is internal: the registry asked for a Basic login and
	// there are no credentials to answer it with. Each caller words it.
	errLoginRequired = errors.New("the registry asks for a login")
)

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

// Client resolves tags to digests. Its zero value is ready to use, and
// anonymous.
type Client struct {
	// HTTP is the client for registry requests; nil means a default one.
	HTTP *http.Client
	// Endpoints maps a registry host ("docker.io", "ghcr.io",
	// "registry.example.com:5000") to its base URL, overriding Endpoint(host).
	// Tests point it at a fake.
	Endpoints map[string]string
	// Credentials returns the user name and secret stored for a registry
	// host. ok is false when there are none. It is called only when a
	// registry asks for a login, so a public image costs no lookup. Nil means
	// anonymous.
	Credentials func(ctx context.Context, host string) (user, pass string, ok bool, err error)

	retryDelays []time.Duration // nil means 0.5 s, then 1 s
}

// endpoint is the base URL of host's registry API.
func (c *Client) endpoint(host string) string {
	if e, ok := c.Endpoints[host]; ok {
		return e
	}
	return Endpoint(host)
}

// session is what one lookup knows about its registry: where it is, and the
// credentials to answer a login with once it asks for one. The secret stays
// in this struct: it goes into a request header and nowhere else, never into
// an error, a log line or a URL.
type session struct {
	host string
	// secure is true when the registry is reached over HTTPS. Credentials are
	// then sent only to HTTPS addresses.
	secure     bool
	user, pass string
	creds      bool // user and pass are set
}

func (c *Client) newSession(host string) *session {
	return &session{host: host, secure: strings.HasPrefix(c.endpoint(host), "https://")}
}

// basic is the Authorization header value for HTTP Basic.
func (s *session) basic() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(s.user+":"+s.pass))
}

// login loads the stored credentials into s, if there are any.
func (c *Client) login(ctx context.Context, s *session) error {
	if c.Credentials == nil {
		return nil
	}
	user, pass, ok, err := c.Credentials(ctx, s.host)
	if err != nil {
		return fmt.Errorf("%w: read the stored credentials for %s: %w", ErrUnavailable, s.host, err)
	}
	if ok {
		s.user, s.pass, s.creds = user, pass, true
	}
	return nil
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
// The answer is nil, or an error wrapping ErrNotFound, ErrUnauthorized or
// ErrUnavailable.
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
	manifestURL := c.endpoint(ref.Registry) + "/v2/" + ref.Repository + "/manifests/" + url.PathEscape(reference)

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

// notFoundOrPrivate is the one message for "missing or private" when no
// credentials were sent: it says what to do about it.
func notFoundOrPrivate(host string) error {
	return fmt.Errorf("%w, or private: add credentials for %s in Settings › Registries", ErrNotFound, host)
}

// notFound is a 404. With credentials sent, "private" is no longer the likely
// reason, so the message doesn't ask for credentials again.
func notFound(s *session) error {
	if s.creds {
		return fmt.Errorf("%w, or the stored credentials for %s can't read it", ErrNotFound, s.host)
	}
	return notFoundOrPrivate(s.host)
}

// refused is a 401 or 403 after the login step: the stored credentials were
// refused when any were sent, else the image is missing or private.
func refused(s *session, code int) error {
	if s.creds {
		return fmt.Errorf("%w (%s answered %d)", ErrUnauthorized, s.host, code)
	}
	return notFoundOrPrivate(s.host)
}

// statusError turns a status nothing else handled into ErrUnavailable. who
// is what answered ("the registry", "the token service"). retry says whether
// another attempt could help.
func statusError(code int, who string) (retry bool, err error) {
	switch {
	case code == http.StatusTooManyRequests:
		return false, fmt.Errorf("%w: %s is rate-limiting requests (429); try again later", ErrUnavailable, who)
	case code >= 500:
		return true, fmt.Errorf("%w: %s answered %d", ErrUnavailable, who, code)
	}
	return false, fmt.Errorf("%w: %s gave an unexpected answer (%d)", ErrUnavailable, who, code)
}

// try makes one attempt: HEAD the manifest, answer the registry's login
// challenge if it makes one, and read the digest. retry says whether another
// attempt could help.
func (c *Client) try(ctx context.Context, ref Reference, manifestURL string) (digest string, retry bool, err error) {
	s := c.newSession(ref.Registry)
	resp, err := c.request(ctx, http.MethodHead, manifestURL, "")
	if err != nil {
		return "", retryable(err), err
	}
	resp.Body.Close()

	authorization := ""
	if resp.StatusCode == http.StatusUnauthorized {
		authorization, retry, err = c.authorize(ctx, s, resp, "repository:"+ref.Repository+":pull")
		if errors.Is(err, errLoginRequired) {
			err = notFoundOrPrivate(s.host)
		}
		if err != nil {
			return "", retry, err
		}
		if resp, err = c.request(ctx, http.MethodHead, manifestURL, authorization); err != nil {
			return "", retryable(err), err
		}
		resp.Body.Close()
	}

	switch code := resp.StatusCode; {
	case code == http.StatusOK:
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return "", false, refused(s, code)
	case code == http.StatusNotFound:
		return "", false, notFound(s)
	default:
		retry, err := statusError(code, "the registry")
		return "", retry, err
	}

	digest = resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		// Some registries leave the header out of HEAD answers; the digest
		// is then the SHA-256 of the manifest itself.
		if digest, err = c.hashManifest(ctx, manifestURL, authorization); err != nil {
			return "", retryable(err), err
		}
	}
	if !digestRE.MatchString(digest) {
		return "", false, fmt.Errorf("%w: it answered with a digest we can't pin (%q)", ErrUnavailable, digest)
	}
	return digest, false, nil
}

func (c *Client) hashManifest(ctx context.Context, manifestURL, authorization string) (string, error) {
	resp, err := c.request(ctx, http.MethodGet, manifestURL, authorization)
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

// Test checks that host's registry answers and accepts the credentials stored
// for it: GET <endpoint>/v2/, answering the login challenge the way a pull
// does. For a Bearer challenge it asks the token service for a token with no
// scope, which only a valid login gets from Docker Hub, GHCR and GitLab. It
// returns nil only when the registry's last answer is 200. Otherwise the error
// wraps ErrUnauthorized (the credentials were refused) or ErrUnavailable
// (unreachable, no HTTPS, not a registry), or says that the registry wants a
// login and none is stored. It makes one attempt and never retries.
func (c *Client) Test(ctx context.Context, host string) error {
	s := c.newSession(host)
	err := c.test(ctx, s)
	if err != nil && !s.creds && (errors.Is(err, errLoginRequired) || errors.Is(err, ErrNotFound)) {
		return fmt.Errorf("%s asks for a login, and no credentials are stored for it", host)
	}
	return err
}

func (c *Client) test(ctx context.Context, s *session) error {
	target := c.endpoint(s.host) + "/v2/"
	resp, err := c.request(ctx, http.MethodGet, target, "")
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		authorization, _, err := c.authorize(ctx, s, resp, "")
		if err != nil {
			return err
		}
		if resp, err = c.request(ctx, http.MethodGet, target, authorization); err != nil {
			return err
		}
		resp.Body.Close()
	}

	switch code := resp.StatusCode; {
	case code == http.StatusOK:
		return nil
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return refused(s, code)
	case code == http.StatusNotFound:
		return fmt.Errorf("%w: %s didn't answer as a container registry", ErrUnavailable, s.host)
	}
	_, err = statusError(resp.StatusCode, "the registry")
	return err
}

// authorize answers a 401: it returns the Authorization header value to
// repeat the request with. scope is the Bearer token's scope; "" asks for a
// token with none. The stored credentials are loaded only here, and used only
// for the challenge the registry itself made: Bearer (sent to the token
// service) or Basic. errLoginRequired means a Basic login with no credentials
// to give.
func (c *Client) authorize(ctx context.Context, s *session, resp *http.Response, scope string) (authorization string, retry bool, err error) {
	ch, ok := pickChallenge(resp.Header.Values("WWW-Authenticate"))
	if !ok {
		// A registry names its API version on every answer. Without that and
		// without a challenge, this isn't one.
		if resp.Header.Get("Docker-Distribution-Api-Version") == "" {
			return "", false, fmt.Errorf("%w: %s didn't answer as a container registry", ErrUnavailable, s.host)
		}
		return "", false, notFoundOrPrivate(s.host)
	}
	if err := c.login(ctx, s); err != nil {
		return "", false, err
	}
	if ch.scheme == "basic" {
		if !s.creds {
			return "", false, errLoginRequired
		}
		return s.basic(), false, nil
	}
	token, retry, err := c.token(ctx, s, ch, scope)
	if err != nil {
		return "", retry, err
	}
	return "Bearer " + token, false, nil
}

// client is the HTTP client for registry requests.
func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// request sends one request with this Authorization header value (none when
// ""). Network errors (and an expired ctx) come back as ErrUnavailable.
func (c *Client) request(ctx context.Context, method, target, authorization string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Accept", acceptManifests)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, transportError(req.URL.Host, "", err)
	}
	return resp, nil
}

// tlsError is a failed TLS handshake: the registry isn't serving HTTPS, or its
// certificate isn't one we trust. Trying again would fail the same way.
type tlsError struct {
	host string
	err  error // the cause, without the request's URL
}

func (e *tlsError) Error() string {
	return fmt.Sprintf("%v: %s must serve the registry API over HTTPS (%v)", ErrUnavailable, e.host, e.err)
}

// Unwrap returns both errors, so errors.Is finds ErrUnavailable and the cause.
func (e *tlsError) Unwrap() []error { return []error{ErrUnavailable, e.err} }

// isTLSError reports whether err, from an HTTP client, is a failed TLS
// handshake.
func isTLSError(err error) bool {
	var record tls.RecordHeaderError
	var cert *tls.CertificateVerificationError
	return errors.Is(err, http.ErrSchemeMismatch) || errors.As(err, &record) || errors.As(err, &cert) ||
		strings.Contains(err.Error(), "tls: ")
}

// retryable says whether another attempt after err could help: not after a
// failed TLS handshake.
func retryable(err error) bool {
	var te *tlsError
	return !errors.As(err, &te)
}

// transportError describes a failed round trip to host. what, when set, says
// which request it was ("token request: "). The error names the URL (no
// secrets in it) and the cause.
func transportError(host, what string, err error) error {
	if isTLSError(err) {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &tlsError{host: host, err: err}
	}
	return fmt.Errorf("%w: %s%v", ErrUnavailable, what, err)
}

// challenge is a parsed `WWW-Authenticate` header.
type challenge struct{ scheme, realm, service string }

// pickChallenge reads the headers of a 401 and picks the one to answer:
// Bearer if the registry offers it, else Basic.
func pickChallenge(headers []string) (challenge, bool) {
	var basic challenge
	haveBasic := false
	for _, h := range headers {
		ch, ok := parseChallenge(h)
		switch {
		case !ok:
		case ch.scheme == "bearer":
			return ch, true
		case !haveBasic:
			basic, haveBasic = ch, true
		}
	}
	return basic, haveBasic
}

// parseChallenge reads `Bearer realm="…",service="…",scope="…"` or
// `Basic realm="…"`. A Bearer challenge without a realm has nowhere to ask
// for a token, so it is not usable.
func parseChallenge(header string) (challenge, bool) {
	scheme, params, _ := strings.Cut(strings.TrimSpace(header), " ")
	ch := challenge{scheme: strings.ToLower(scheme)}
	if ch.scheme != "bearer" && ch.scheme != "basic" {
		return challenge{}, false
	}
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
	return ch, ch.scheme == "basic" || ch.realm != ""
}

// token gets a pull token for scope from the token service the Bearer
// challenge names; with no scope, a token for nothing in particular (what
// Test needs). When s has credentials the request carries them as Basic auth,
// and only to an HTTPS address if the registry itself is HTTPS. The token is
// never logged or put in an error.
func (c *Client) token(ctx context.Context, s *session, ch challenge, scope string) (token string, retry bool, err error) {
	u, err := url.Parse(ch.realm)
	if err != nil {
		return "", false, fmt.Errorf("%w: bad token realm %q", ErrUnavailable, ch.realm)
	}
	if s.creds && s.secure && u.Scheme != "https" {
		return "", false, fmt.Errorf("%w: %s sent the credentials' login to a plain-HTTP address, so they were not sent", ErrUnavailable, s.host)
	}
	q := u.Query()
	if ch.service != "" {
		q.Set("service", ch.service)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if s.creds {
		req.Header.Set("Authorization", s.basic())
	}
	resp, err := c.client().Do(req)
	if err != nil {
		err = transportError(u.Host, "token request: ", err)
		return "", retryable(err), err
	}
	defer resp.Body.Close()

	switch code := resp.StatusCode; {
	case code == http.StatusOK:
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return "", false, refused(s, code)
	default:
		retry, err := statusError(code, "the token service")
		return "", retry, err
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
