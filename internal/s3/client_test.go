package s3_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/s3"
	"github.com/cosmic-chimps/caravana/internal/s3/s3test"
)

func newClient(t *testing.T) (*s3.Client, *s3test.Server) {
	t.Helper()
	srv := s3test.New()
	t.Cleanup(srv.Close)
	return s3.New(srv.Config(), nil), srv
}

func tempFile(t *testing.T, data []byte) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPutGetDeleteRoundTrip(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("caravana "), 5000)
	key := "my prefix/_caravana/1700000000/caravana-1700000000.cvbackup" // a space: the encoding must match the signature
	if err := c.Put(ctx, key, tempFile(t, data), int64(len(data))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, ok := srv.Object(key); !ok || !bytes.Equal(got, data) {
		t.Fatalf("the server holds %d bytes (found %v)", len(got), ok)
	}
	rc, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Errorf("Get returned %d bytes, want %d", len(got), len(data))
	}
	head, err := c.GetRange(ctx, key, 18)
	if err != nil || string(head) != "caravana caravana " {
		t.Errorf("GetRange = %q, %v", head, err)
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := srv.Object(key); ok {
		t.Error("the object is still there")
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Errorf("deleting what isn't there should be done: %v", err)
	}
}

func TestGetRangeOfAShortOrEmptyObject(t *testing.T) {
	c, srv := newClient(t)
	srv.Put("short", []byte("abc"))
	srv.Put("empty", nil)
	if got, err := c.GetRange(context.Background(), "short", 4096); err != nil || string(got) != "abc" {
		t.Errorf("short: %q, %v", got, err)
	}
	if got, err := c.GetRange(context.Background(), "empty", 4096); err != nil || len(got) != 0 {
		t.Errorf("empty: %q, %v", got, err)
	}
}

func TestListFollowsContinuation(t *testing.T) {
	c, srv := newClient(t)
	srv.SetPageSize(2)
	for _, k := range []string{"p/a", "p/b", "p/c", "p/d", "p/e", "other/z"} {
		srv.Put(k, []byte(k))
	}
	got, err := c.List(context.Background(), "p/")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, o := range got {
		keys = append(keys, o.Key)
		if o.Size != int64(len(o.Key)) || o.LastModified.IsZero() {
			t.Errorf("%+v", o)
		}
	}
	if strings.Join(keys, ",") != "p/a,p/b,p/c,p/d,p/e" {
		t.Errorf("keys = %v", keys)
	}
	if srv.Requests() < 3 {
		t.Errorf("the listing took %d requests, want at least 3 pages", srv.Requests())
	}
}

func TestErrorsCarryOnlyOperationStatusAndCode(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	srv.Fail("PUT", http.StatusForbidden)
	err := c.Put(ctx, "k", tempFile(t, []byte("x")), 1)
	var e *s3.Error
	if !errors.As(err, &e) || e.Op != "put" || e.Status != 403 || e.Code != "AccessDenied" {
		t.Fatalf("Put error = %v", err)
	}
	if msg := err.Error(); strings.Contains(msg, "must not repeat") || strings.Contains(msg, srv.URL) || strings.Contains(msg, srv.SecretAccessKey) {
		t.Errorf("the error says too much: %s", msg)
	}
	if _, err := c.Get(ctx, "missing"); !s3.IsNotFound(err) {
		t.Errorf("Get of a missing object = %v, want not found", err)
	}
}

func TestTransportErrorsDropTheURL(t *testing.T) {
	srv := s3test.New()
	cfg := srv.Config()
	srv.Close() // nothing listens now
	c := s3.New(cfg, nil)
	err := c.Delete(context.Background(), "secret-looking/key")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "secret-looking") || strings.Contains(err.Error(), "http://") {
		t.Errorf("the error holds the URL: %v", err)
	}
	var e *s3.Error
	if !errors.As(err, &e) || e.Status != 0 {
		t.Errorf("error = %#v", err)
	}
}

func TestWrongSecretIsRefusedBySignature(t *testing.T) {
	srv := s3test.New()
	defer srv.Close()
	cfg := srv.Config()
	cfg.SecretAccessKey = "another"
	err := s3.New(cfg, nil).Delete(context.Background(), "k")
	var e *s3.Error
	if !errors.As(err, &e) || e.Status != 403 || e.Code != "SignatureDoesNotMatch" {
		t.Errorf("error = %v", err)
	}
}

func TestCanceledContextIsReported(t *testing.T) {
	c, _ := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Delete(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}
