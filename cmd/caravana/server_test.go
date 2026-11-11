package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var nodeTokenFormat = regexp.MustCompile(`^cvnd_[A-Za-z0-9_-]{43}$`)

// serverCmd runs `caravana server <args>` against dir.
func serverCmd(t *testing.T, dir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	args = append(append([]string{}, args...), "-data-dir", dir)
	code = run(context.Background(), append([]string{"server"}, args...), noEnv, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestServerAdoptFirst(t *testing.T) {
	dir := t.TempDir()
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := serverCmd(t, dir, "adopt-first", "--name", "hel-1", "--public-key", pub, "--address", "203.0.113.4")
	token := strings.TrimSpace(stdout)
	if code != 0 || !nodeTokenFormat.MatchString(token) || stdout != token+"\n" || stderr != "" {
		t.Fatalf("adopt-first = %d, stdout %q, stderr %q; want 0 and the node token alone", code, stdout, stderr)
	}
	hashOf := func(tok string) string { sum := sha256.Sum256([]byte(tok)); return hex.EncodeToString(sum[:]) }
	rowOf := func() store.Server {
		st, err := store.Open(context.Background(), dir)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer st.Close()
		rows, err := st.ListServers(context.Background())
		if err != nil || len(rows) != 1 {
			t.Fatalf("servers = %+v, %v", rows, err)
		}
		return rows[0]
	}
	row := rowOf()
	if !row.First || row.Index != 1 || row.Name != "hel-1" || row.State != store.ServerReady || !row.NomadServer ||
		row.PublicKey != pub || row.Endpoint != "203.0.113.4:51820" || row.TokenHash != hashOf(token) {
		t.Errorf("row = %+v", row)
	}
	if strings.Contains(row.TokenHash+row.JoinHash+row.Report, token) {
		t.Error("the token is stored")
	}

	// Again: a new token for the same server; the old one stops working.
	code, stdout, stderr = serverCmd(t, dir, "adopt-first", "--name", "hel-1", "--public-key", pub, "--address", "203.0.113.5")
	token2 := strings.TrimSpace(stdout)
	if code != 0 || !nodeTokenFormat.MatchString(token2) || token2 == token || stderr != "" {
		t.Fatalf("second adopt-first = %d, %q, %q", code, stdout, stderr)
	}
	if row := rowOf(); row.TokenHash != hashOf(token2) || row.Endpoint != "203.0.113.5:51820" {
		t.Errorf("row after the second = %+v", row)
	}
}

func TestServerCommandRefusals(t *testing.T) {
	dir := t.TempDir()
	_, pub, _ := mesh.NewKey()
	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no command", nil, 2, "usage: caravana server"},
		{"unknown command", []string{"frobnicate"}, 2, "unknown command"},
		{"no name", []string{"adopt-first", "--public-key", pub, "--address", "203.0.113.4"}, 2, "--name is required"},
		{"no key", []string{"adopt-first", "--name", "a", "--address", "203.0.113.4"}, 2, "--public-key is required"},
		{"no address", []string{"adopt-first", "--name", "a", "--public-key", pub}, 2, "--address is required"},
		{"bad name", []string{"adopt-first", "--name", "Bad Name", "--public-key", pub, "--address", "203.0.113.4"}, 2, "--name:"},
		{"bad key", []string{"adopt-first", "--name", "a", "--public-key", "short", "--address", "203.0.113.4"}, 2, "--public-key:"},
		{"bad address", []string{"adopt-first", "--name", "a", "--public-key", pub, "--address", "a-name.example.com"}, 2, "--address:"},
		{"an extra argument", []string{"adopt-first", "--name", "a", "--public-key", pub, "--address", "203.0.113.4", "x"}, 2, "unexpected argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var args []string
			if len(c.args) > 0 {
				args = append(args, c.args...)
			}
			var out, errOut bytes.Buffer
			args = append(args, "-data-dir", dir)
			if len(c.args) == 0 {
				args = nil
			}
			code := run(context.Background(), append([]string{"server"}, args...), noEnv, strings.NewReader(""), &out, &errOut)
			if code != c.code || !strings.Contains(errOut.String(), c.want) || out.Len() != 0 {
				t.Errorf("code = %d, stdout %q, stderr %q; want %d and %q on stderr", code, out.String(), errOut.String(), c.code, c.want)
			}
		})
	}
	var out bytes.Buffer
	if code := run(context.Background(), []string{"server", "--help"}, noEnv, strings.NewReader(""), &out, &out); code != 0 || !strings.Contains(out.String(), "adopt-first") {
		t.Errorf("help = %d %q", code, out.String())
	}
}
