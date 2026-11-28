package s3

import (
	"net/http"
	"regexp"
	"testing"
	"time"
)

// AWS's published Signature Version 4 examples for S3 ("Signature Calculations for the Authorization
// Header: Transferring Payload in a Single Chunk"): the keys, the date, the requests and the signatures
// are the documentation's. The signer must give exactly these.
func TestSignRequestKnownAnswers(t *testing.T) {
	creds := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", Region: "us-east-1"}
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, target, rng, signature string
		signedHeaders                string
	}{
		{"GET Object with a range", "https://examplebucket.s3.amazonaws.com/test.txt", "bytes=0-9",
			"f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41", "host;range;x-amz-content-sha256;x-amz-date"},
		{"GET Bucket Lifecycle", "https://examplebucket.s3.amazonaws.com/?lifecycle", "",
			"fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543", "host;x-amz-content-sha256;x-amz-date"},
		{"GET Bucket (List Objects)", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", "",
			"34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7", "host;x-amz-content-sha256;x-amz-date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.rng != "" {
				req.Header.Set("Range", tc.rng)
			}
			SignRequest(req, creds, "", now)
			auth := req.Header.Get("Authorization")
			m := regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})$`).FindStringSubmatch(auth)
			if m == nil {
				t.Fatalf("Authorization = %q", auth)
			}
			if m[1] != tc.signedHeaders {
				t.Errorf("signed headers = %q, want %q", m[1], tc.signedHeaders)
			}
			if m[2] != tc.signature {
				t.Errorf("signature = %s, want %s", m[2], tc.signature)
			}
		})
	}
}

func TestEncode(t *testing.T) {
	for _, tc := range []struct {
		in    string
		slash bool
		want  string
	}{
		{"a/b c", false, "a/b%20c"},
		{"a/b c", true, "a%2Fb%20c"},
		{"AZaz09-_.~", true, "AZaz09-_.~"},
		{"é+=", false, "%C3%A9%2B%3D"},
	} {
		if got := encode(tc.in, tc.slash); got != tc.want {
			t.Errorf("encode(%q, %v) = %q, want %q", tc.in, tc.slash, got, tc.want)
		}
	}
}
