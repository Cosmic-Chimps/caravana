package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrBadAddress is returned for an address that is not an absolute http or
// https URL. It never carries the address, because a Slack or Discord URL is
// itself a secret and Go's own URL errors quote it.
var ErrBadAddress = errors.New("the address is not an absolute http or https URL")

// ParseAddress parses raw as the address of a channel: an absolute http or
// https URL with a host. The error is always ErrBadAddress, never the text
// of raw.
func ParseAddress(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrBadAddress
	}
	return u, nil
}

// Render builds the request that delivers ev to ch, following research R4 of
// feature 018. deliveryID and now are inputs, not looked up, so the function is
// pure: the webhook's Caravana-Delivery header and the timestamp under its
// signature come from them, and the same input always gives the same request.
//
// The request has no context; Send adds the timeout.
func Render(ch Channel, ev Event, deliveryID string, now time.Time) (*http.Request, error) {
	if _, err := ParseAddress(ch.Address); err != nil {
		return nil, fmt.Errorf("render a %s message: %w", ch.Kind, err)
	}

	switch ch.Kind {
	case KindWebhook:
		return renderWebhook(ch, ev, deliveryID, now)
	case KindSlack:
		return renderChat(ch, "text", "*", ev)
	case KindDiscord:
		return renderChat(ch, "content", "**", ev)
	case KindNtfy:
		return renderNtfy(ch, ev)
	}
	return nil, fmt.Errorf("render a message: unknown channel kind %q", ch.Kind)
}

// webhookBody is the JSON a webhook receives: the delivery's id, then the event
// itself. Embedding Event puts its fields at the same level in the JSON.
type webhookBody struct {
	ID string `json:"id"`
	Event
}

func renderWebhook(ch Channel, ev Event, deliveryID string, now time.Time) (*http.Request, error) {
	if ch.Secret == "" {
		return nil, errors.New("render a webhook message: the channel has no signing secret")
	}
	body, err := marshal(webhookBody{ID: deliveryID, Event: ev})
	if err != nil {
		return nil, fmt.Errorf("render a webhook message: %w", err)
	}
	req, err := newPost(ch.Address, body, "application/json")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Caravana-Event", ev.Type)
	req.Header.Set("Caravana-Delivery", deliveryID)
	req.Header.Set(SignatureHeader, Sign(ch.Secret, now.Unix(), body))
	return req, nil
}

// renderChat renders the Slack and Discord messages, which differ only in the
// JSON field that holds the text and the markup for bold: the title in bold,
// then the text, then the link when there is one.
func renderChat(ch Channel, field, bold string, ev Event) (*http.Request, error) {
	text := bold + ev.Title + bold + "\n" + withDetail(ev)
	if ev.URL != "" {
		text += "\n" + ev.URL
	}
	body, err := marshal(map[string]string{field: text})
	if err != nil {
		return nil, fmt.Errorf("render a %s message: %w", ch.Kind, err)
	}
	return newPost(ch.Address, body, "application/json")
}

// renderNtfy renders an ntfy publish: the text is the body and everything else
// travels in the headers ntfy documents for publishing. Problems and failures
// are "high" priority with a warning tag; resolved problems, successful
// deploys and tests are default priority with a check mark.
func renderNtfy(ch Channel, ev Event) (*http.Request, error) {
	req, err := newPost(ch.Address, []byte(withDetail(ev)), "text/plain; charset=utf-8")
	if err != nil {
		return nil, err
	}
	bad := ev.Type == EventProblemOpened || ev.Type == EventDeployFailed
	tags, priority := "white_check_mark", "default"
	if bad {
		tags, priority = "warning", "high"
	}
	req.Header.Set("Title", oneLine(ev.Title)) // a header cannot hold a line break
	req.Header.Set("Tags", tags)
	req.Header.Set("Priority", priority)
	if ev.URL != "" {
		req.Header.Set("Click", oneLine(ev.URL))
	}
	if ch.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+ch.Secret)
	}
	return req, nil
}

// newPost builds a POST of body to address. The address was parsed by Render
// already, so an error here is reported without it.
func newPost(address string, body []byte, contentType string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return nil, ErrBadAddress // http's own error would quote the address
	}
	req.Header.Set("Content-Type", contentType)
	return req, nil
}

// marshal encodes v as JSON without escaping "<", ">" and "&", so a message
// body reads as written: the default encoding turns them into < and
// friends, which receivers decode but a human reading a log does not enjoy.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// maxDetail is how many characters of the raw detail a chat message carries.
// The webhook's JSON has the whole detail; a chat message stays readable.
const maxDetail = 500

// withDetail is the text of a Slack, Discord or ntfy message: the event's text,
// then, for a deploy that came from CI, a line saying from where ("From CI:
// ..."), for a problem about a schedule its name and exit code ("Schedule:
// ..."), then the raw reason on a line of its own ("Detail: ..."), so the
// message says what went wrong and not only that something did (FR-006). The
// detail is put on one line and cut at maxDetail characters.
func withDetail(ev Event) string {
	text := ev.Text
	if line := originLine(ev.Origin); line != "" {
		text += "\n" + line
	}
	if line := scheduleLine(ev); line != "" {
		text += "\n" + line
	}
	if line := domainLine(ev); line != "" {
		text += "\n" + line
	}
	detail := strings.Join(strings.Fields(ev.Detail), " ")
	if detail == "" {
		return text
	}
	if r := []rune(detail); len(r) > maxDetail {
		detail = string(r[:maxDetail]) + "…"
	}
	return text + "\nDetail: " + detail
}

// scheduleLine is "Schedule: rates-sync, exit code 3" for an event about a
// schedule (the exit code when its run exited with one), or "" for another.
func scheduleLine(ev Event) string {
	if ev.Schedule == "" {
		return ""
	}
	line := "Schedule: " + oneLine(ev.Schedule)
	if ev.ExitCode != nil {
		line += fmt.Sprintf(", exit code %d", *ev.ExitCode)
	}
	return line
}

// domainLine is "Domain: shop.example.com, points at 198.51.100.9" or
// "Domain: shop.example.com, certificate expires 2026-11-03 09:00 UTC" for an
// event about a domain, or "" for another.
func domainLine(ev Event) string {
	if ev.Domain == "" {
		return ""
	}
	line := "Domain: " + oneLine(ev.Domain)
	if ev.AddressSeen != "" {
		line += ", points at " + oneLine(ev.AddressSeen)
	}
	if ev.ExpiresAt != nil {
		line += ", certificate expires " + ev.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	return line
}

// originLine is "From CI: abc1234 on main by ana, run https://...": the short
// commit, the branch or tag, who pushed and the run, whichever were sent. It is
// "" for no origin.
func originLine(o *Origin) string {
	if o == nil {
		return ""
	}
	line := "From CI:"
	if o.Commit != "" {
		line += " " + o.Commit[:min(len(o.Commit), 7)]
	}
	ref := strings.TrimPrefix(strings.TrimPrefix(o.Ref, "refs/heads/"), "refs/tags/")
	if ref != "" {
		line += " on " + oneLine(ref)
	}
	if o.Actor != "" {
		line += " by " + oneLine(o.Actor)
	}
	if o.Run != "" {
		line += ", run " + oneLine(o.Run)
	}
	if line == "From CI:" {
		return ""
	}
	return line
}

// oneLine replaces line breaks with spaces, for values that go into headers.
func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
