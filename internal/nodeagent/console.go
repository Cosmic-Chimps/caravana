package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ConsoleError is a refusal the console answered, with its own words.
type ConsoleError struct {
	Status  int
	Code    string
	Message string
}

func (e *ConsoleError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("the console answered HTTP %d", e.Status)
}

// softError is a failure that changes nothing and isn't an error of the command: the console can't
// be reached, or answered something the agent can't act on. The timer tries again.
type softError struct{ msg string }

func (e *softError) Error() string { return e.msg }

var joinTokenRE = regexp.MustCompile(`^cvjn_[A-Za-z0-9_-]{43}$`)

// ValidJoinToken says s has the shape of a join token: cvjn_ and 43 characters of [A-Za-z0-9_-].
func ValidJoinToken(s string) bool { return joinTokenRE.MatchString(s) }

// ValidConsoleURL says s is https:// and a host name (with an optional port), or http:// for
// 127.0.0.1 and localhost only. No path, query or credentials.
func ValidConsoleURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return fmt.Errorf("the console's address must look like https://caravana.example.com")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" {
			return fmt.Errorf("http:// is only for 127.0.0.1 and localhost; use https://")
		}
	default:
		return fmt.Errorf("the console's address must start with https://")
	}
	return nil
}

// postJSON posts a JSON body to the console and returns the status and the body (at most 256 KiB).
// bearer, when not empty, is the node token. Errors never hold the body or the token.
func (a *Agent) postJSON(ctx context.Context, base, path, bearer string, body any) (int, []byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read the console's answer: %w", err)
	}
	return resp.StatusCode, data, nil
}

// consoleError reads the usual {"error": {"code", "message", "field"}} body.
func consoleError(status int, body []byte) *ConsoleError {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	ce := &ConsoleError{Status: status}
	if json.Unmarshal(body, &e) == nil {
		ce.Code = clean(e.Error.Code)
		ce.Message = clean(e.Error.Message)
	}
	return ce
}
