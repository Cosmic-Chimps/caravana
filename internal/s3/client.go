// Package s3 is a small client for the S3-compatible buckets Caravana's backups go to: path-style
// addressing and Signature Version 4, over net/http. It does what Caravana's own backup needs (put a file,
// read an object or its first bytes, list a prefix, delete) and nothing else, so no SDK is needed.
//
// No error it returns holds a URL, a header, a key, a signature or a body: only the operation, the HTTP
// status and the Code S3 gave, so an error can be logged and shown.
package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config says which bucket to reach and with which key pair.
type Config struct {
	Endpoint string // https://s3.example.com, or http:// for a loopback or lab address
	Region   string // "" is us-east-1
	Bucket   string
	Credentials
}

// Client reads and writes one bucket. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
	now  func() time.Time
}

// New returns a client. A nil hc is a client with no overall timeout (an upload may be long: callers
// bound it with a context).
func New(cfg Config, hc *http.Client) *Client {
	cfg.Credentials.Region = cfg.Region
	if hc == nil {
		hc = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), CheckRedirect: noRedirect}
	}
	return &Client{cfg: cfg, http: hc, now: time.Now}
}

// noRedirect keeps a signed request from being sent to another host.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Error is a failed operation: Op is "put", "get", "list" or "delete"; Status the HTTP status, 0 when
// the request never got an answer; Code S3's error code ("AccessDenied", "NoSuchKey"), possibly empty.
// Err is the transport's cause when there was no answer, with the URL taken out.
type Error struct {
	Op     string
	Status int
	Code   string
	Err    error
}

func (e *Error) Error() string {
	switch {
	case e.Status == 0:
		return fmt.Sprintf("s3 %s: no answer: %v", e.Op, e.Err)
	case e.Code != "":
		return fmt.Sprintf("s3 %s: HTTP %d (%s)", e.Op, e.Status, e.Code)
	}
	return fmt.Sprintf("s3 %s: HTTP %d", e.Op, e.Status)
}

func (e *Error) Unwrap() error { return e.Err }

// NotFound says the object isn't there.
func (e *Error) NotFound() bool { return e.Status == http.StatusNotFound }

// IsNotFound says err is a "no such object" answer.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.NotFound()
}

// Object is one entry of a listing.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// objectURL is the address of key, path-style: <endpoint>/<bucket>/<key>, written already encoded.
func (c *Client) objectURL(key string) string {
	base := strings.TrimRight(c.cfg.Endpoint, "/")
	return base + "/" + encode(c.cfg.Bucket, false) + "/" + encode(key, false)
}

func (c *Client) bucketURL(query string) string {
	u := strings.TrimRight(c.cfg.Endpoint, "/") + "/" + encode(c.cfg.Bucket, false)
	if query != "" {
		u += "?" + query
	}
	return u
}

// do signs and sends a request. body is nil or a ReadSeeker of size bytes whose SHA-256 is payloadHash.
func (c *Client) do(ctx context.Context, op, method, target string, header http.Header, body io.ReadSeeker, size int64, payloadHash string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = body
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, &Error{Op: op, Err: errors.New("the endpoint isn't a usable address")}
	}
	if body != nil {
		req.ContentLength = size
		req.GetBody = nil
	}
	for k, v := range header {
		req.Header[k] = v
	}
	SignRequest(req, c.cfg.Credentials, payloadHash, c.now())
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drops the URL
		}
		return nil, &Error{Op: op, Err: err}
	}
	return resp, nil
}

// failure turns a non-success response into an *Error and closes it. Only S3's Code is read from the
// body.
func failure(op string, resp *http.Response) *Error {
	defer resp.Body.Close()
	var body struct {
		Code string `xml:"Code"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = xml.Unmarshal(data, &body)
	code := body.Code
	if len(code) > 64 || strings.ContainsAny(code, " \t\r\n<>") {
		code = ""
	}
	return &Error{Op: op, Status: resp.StatusCode, Code: code}
}

// Put stores size bytes read from r under key. r is read twice (the payload's SHA-256 is signed) and left
// at its end; it should be a file.
func (c *Client) Put(ctx context.Context, key string, r io.ReadSeeker, size int64) error {
	h := sha256.New()
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return &Error{Op: "put", Err: err}
	}
	if n, err := io.Copy(h, r); err != nil || n != size {
		return &Error{Op: "put", Err: fmt.Errorf("the file to upload is not %d bytes long", size)}
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return &Error{Op: "put", Err: err}
	}
	resp, err := c.do(ctx, "put", http.MethodPut, c.objectURL(key), nil, r, size, hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return failure("put", resp)
	}
	resp.Body.Close()
	return nil
}

// Get returns the object's body; the caller closes it. A missing object is an *Error with NotFound true.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, "get", http.MethodGet, c.objectURL(key), nil, nil, 0, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, failure("get", resp)
	}
	return resp.Body, nil
}

// GetRange returns up to n bytes from the start of the object, for a header. An object shorter than n
// answers what it has.
func (c *Client) GetRange(ctx context.Context, key string, n int64) ([]byte, error) {
	h := http.Header{"Range": {"bytes=0-" + strconv.FormatInt(n-1, 10)}}
	resp, err := c.do(ctx, "get", http.MethodGet, c.objectURL(key), h, nil, 0, "")
	if err != nil {
		return nil, err
	}
	// An empty object answers 416: it has no first bytes.
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		resp.Body.Close()
		return nil, nil
	}
	if resp.StatusCode/100 != 2 {
		return nil, failure("get", resp)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, n))
	if err != nil {
		return nil, &Error{Op: "get", Err: err}
	}
	return data, nil
}

// Delete removes key. An object that isn't there is done.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, "delete", http.MethodDelete, c.objectURL(key), nil, nil, 0, "")
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return failure("delete", resp)
	}
	resp.Body.Close()
	return nil
}

// maxListPages bounds a listing, so a server that never stops saying "more" can't hold a run.
const maxListPages = 1000

// List returns every object whose key starts with prefix, following the continuation tokens of
// ListObjectsV2. The order is the server's (ascending by key).
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for range maxListPages {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		// The query is written encoded by hand so that what is sent is what is signed.
		resp, err := c.do(ctx, "list", http.MethodGet, c.bucketURL(canonicalQuery(q)), nil, nil, 0, "")
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			return nil, failure("list", resp)
		}
		var page struct {
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
			Contents              []struct {
				Key          string `xml:"Key"`
				Size         int64  `xml:"Size"`
				LastModified string `xml:"LastModified"`
			} `xml:"Contents"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, &Error{Op: "list", Status: resp.StatusCode, Err: errors.New("the listing isn't readable")}
		}
		for _, o := range page.Contents {
			t, _ := time.Parse(time.RFC3339, o.LastModified)
			out = append(out, Object{Key: o.Key, Size: o.Size, LastModified: t.UTC()})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		token = page.NextContinuationToken
	}
	return nil, &Error{Op: "list", Err: errors.New("the listing doesn't end")}
}
