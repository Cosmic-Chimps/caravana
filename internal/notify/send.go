package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// sendTimeout is how long one delivery may take, from connecting to the end of
// the answer (FR-009). A receiver that is slower fails the attempt.
const sendTimeout = 10 * time.Second

// errorBodyLimit is how many bytes of a receiver's answer an error keeps.
const errorBodyLimit = 200

// Send delivers req with client and reports whether the receiver accepted it:
// any 2xx answer is success, anything else, or no answer within ten seconds, is
// an error. A non-2xx error carries the status and up to 200 bytes of the
// answer, which is what the console lists next to a failed delivery.
//
// A Slack or Discord URL is a secret, so no error holds the URL: they name the
// host only, and a receiver that echoes the path back has it blanked out of the
// answer. Redirects are not followed, so a message and its signature cannot be
// bounced to a host the owner never chose; a 3xx is reported as a failure.
func Send(ctx context.Context, client *http.Client, req *http.Request) error {
	return send(ctx, client, req, sendTimeout)
}

// send is Send with the timeout as a parameter, so tests need not wait ten
// seconds.
func send(ctx context.Context, client *http.Client, req *http.Request, timeout time.Duration) error {
	if client == nil {
		client = http.DefaultClient
	}
	host := req.URL.Host

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// A copy of the client, so changing its redirect rule does not touch the
	// caller's.
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		// *url.Error quotes the whole URL; keep only what went wrong.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return fmt.Errorf("send to %s: %w", host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("send to %s: the receiver answered %s%s", host, resp.Status, answerSuffix(string(snippet), req.URL))
}

// answerSuffix formats the start of a receiver's answer for an error: empty for
// no body, otherwise ": <text>" on one line, with the request's path and query
// blanked out in case the receiver echoed them.
func answerSuffix(body string, u *url.URL) string {
	body = strings.ToValidUTF8(body, "")
	for _, secret := range []string{u.RawQuery, u.EscapedPath(), u.Path} {
		if len(secret) > 1 { // "/" alone is no secret, and "" would match everywhere
			body = strings.ReplaceAll(body, secret, "…")
		}
	}
	body = strings.Join(strings.Fields(body), " ")
	if len(body) > errorBodyLimit {
		// Cut on a rune boundary so the error stays valid UTF-8.
		body = strings.ToValidUTF8(body[:errorBodyLimit], "")
	}
	if body == "" {
		return ""
	}
	return ": " + body
}
