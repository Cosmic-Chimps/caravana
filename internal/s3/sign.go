package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Signature Version 4, the way S3 wants it (the AWS documentation, "Authenticating Requests: Using the
// Authorization Header"). Only what Caravana's requests need: path-style URLs, the headers host, range,
// content-type, content-md5 and x-amz-*, and the payload's SHA-256 always signed.

const (
	algorithm = "AWS4-HMAC-SHA256"
	amzDate   = "20060102T150405Z"
	shortDate = "20060102"
	service   = "s3"
	emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// Credentials are the key pair and region a request is signed with.
type Credentials struct {
	AccessKeyID, SecretAccessKey, Region string
}

// SignRequest adds the x-amz-date and x-amz-content-sha256 headers and the Authorization header to req.
// payloadHash is the hex SHA-256 of the body ("" for none). It is exported for the fake server of s3test,
// which checks a request by signing it again; the known-answer test of this package pins it to AWS's
// published examples.
func SignRequest(req *http.Request, c Credentials, payloadHash string, now time.Time) {
	if payloadHash == "" {
		payloadHash = emptyHash
	}
	now = now.UTC()
	req.Header.Set("X-Amz-Date", now.Format(amzDate))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signed, canonical := canonicalRequest(req, payloadHash)
	scope := now.Format(shortDate) + "/" + c.region() + "/" + service + "/aws4_request"
	toSign := algorithm + "\n" + now.Format(amzDate) + "\n" + scope + "\n" + hexSHA256([]byte(canonical))
	signature := hex.EncodeToString(hmacSHA256(signingKey(c, now), toSign))
	req.Header.Set("Authorization", algorithm+" Credential="+c.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+signature)
}

func (c Credentials) region() string {
	if c.Region == "" {
		return "us-east-1"
	}
	return c.Region
}

// signingKey is the key derived for the day, region and service.
func signingKey(c Credentials, now time.Time) []byte {
	k := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), now.Format(shortDate))
	k = hmacSHA256(k, c.region())
	k = hmacSHA256(k, service)
	return hmacSHA256(k, "aws4_request")
}

// canonicalRequest returns the signed header names and the canonical request of req.
func canonicalRequest(req *http.Request, payloadHash string) (signedHeaders, canonical string) {
	headers := map[string]string{"host": req.Host}
	if req.Host == "" {
		headers["host"] = req.URL.Host
	}
	for name, values := range req.Header {
		n := strings.ToLower(name)
		if n == "range" || n == "content-type" || n == "content-md5" || strings.HasPrefix(n, "x-amz-") {
			headers[n] = strings.Join(trimAll(values), ",")
		}
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var h strings.Builder
	for _, n := range names {
		h.WriteString(n + ":" + headers[n] + "\n")
	}
	signedHeaders = strings.Join(names, ";")
	canonical = strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL.Query()),
		h.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	return signedHeaders, canonical
}

func trimAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return out
}

// canonicalPath is the request's path as sent, which the client wrote already encoded (S3's paths are
// encoded once).
func canonicalPath(u *url.URL) string {
	if p := u.EscapedPath(); p != "" {
		return p
	}
	return "/"
}

// canonicalQuery is the query's names and values encoded and sorted by name.
func canonicalQuery(q url.Values) string {
	pairs := make([]string, 0, len(q))
	for name, values := range q {
		for _, v := range values {
			pairs = append(pairs, encode(name, true)+"="+encode(v, true))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// encode is the URI encoding S3 asks for: everything but letters, digits and "-_.~" is %XX, and "/" too
// when slash is true.
func encode(s string, slash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !slash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
