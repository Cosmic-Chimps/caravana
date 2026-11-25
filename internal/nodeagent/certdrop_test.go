package nodeagent

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	dropID1 = "0123456789abcdef0123456789abcdef"
	dropID2 = "fedcba9876543210fedcba9876543210"

	// sentinelPEM is what a certificate and a key of the fake store hold: it must never leave the folder.
	sentinelCertificate = "c2VjcmV0LWNlcnRpZmljYXRlLXNlbnRpbmVsLTA1Ng=="
	sentinelKey         = "c2VjcmV0LWtleS1zZW50aW5lbC0wNTY="
	sentinelAccount     = "c2VjcmV0LWFjY291bnQta2V5LXNlbnRpbmVsLTA1Ng=="
)

// acmeCert is one stored certificate as Traefik writes it.
func acmeCert(main string, sans ...string) map[string]any {
	domain := map[string]any{"main": main}
	if len(sans) > 0 {
		domain["sans"] = sans
	}
	return map[string]any{"domain": domain, "certificate": sentinelCertificate + "-" + main, "key": sentinelKey + "-" + main, "Store": "default"}
}

// acmeStore is a store file: one resolver per name given, each with the account and certificates.
func acmeStore(t *testing.T, resolvers map[string][]map[string]any) []byte {
	t.Helper()
	top := map[string]any{}
	for name, certs := range resolvers {
		top[name] = map[string]any{
			"Account":      map[string]any{"Email": "ana@example.com", "PrivateKey": sentinelAccount, "KeyType": "4096"},
			"Certificates": certs,
			"Extra":        map[string]any{"kept": true},
		}
	}
	b, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// storeHosts lists "main" of every certificate of a store, per resolver.
func storeHosts(t *testing.T, store []byte) map[string][]string {
	t.Helper()
	var top map[string]struct {
		Certificates []struct {
			Domain struct {
				Main string   `json:"main"`
				Sans []string `json:"sans"`
			} `json:"domain"`
		}
	}
	if err := json.Unmarshal(store, &top); err != nil {
		t.Fatalf("the store isn't JSON: %v", err)
	}
	out := map[string][]string{}
	for name, r := range top {
		out[name] = []string{}
		for _, c := range r.Certificates {
			out[name] = append(out[name], strings.Join(append([]string{c.Domain.Main}, c.Domain.Sans...), "+"))
		}
	}
	return out
}

func TestDropCovered(t *testing.T) {
	tests := []struct {
		name    string
		store   map[string][]map[string]any
		names   []string
		dropped []string
		kept    map[string][]string
	}{
		{"an exact name", map[string][]map[string]any{"le": {acmeCert("a.example.com"), acmeCert("other.test")}},
			[]string{"a.example.com"}, []string{"a.example.com"}, map[string][]string{"le": {"other.test"}}},
		{"a wildcard covers one label more", map[string][]map[string]any{"le": {acmeCert("a.example.com"), acmeCert("b.example.com")}},
			[]string{"*.example.com"}, []string{"a.example.com", "b.example.com"}, map[string][]string{"le": {}}},
		{"two labels more aren't covered", map[string][]map[string]any{"le": {acmeCert("x.a.example.com")}},
			[]string{"*.example.com"}, nil, map[string][]string{"le": {"x.a.example.com"}}},
		{"the wildcard's own name isn't covered", map[string][]map[string]any{"le": {acmeCert("example.com")}},
			[]string{"*.example.com"}, nil, map[string][]string{"le": {"example.com"}}},
		{"a certificate with a name no upload covers stays", map[string][]map[string]any{"le": {acmeCert("a.example.com", "www.other.test")}},
			[]string{"a.example.com"}, nil, map[string][]string{"le": {"a.example.com+www.other.test"}}},
		{"every name covered: main first, then the sans", map[string][]map[string]any{"le": {acmeCert("a.example.com", "www.example.com")}},
			[]string{"a.example.com", "*.example.com"}, []string{"a.example.com+www.example.com"}, map[string][]string{"le": {}}},
		{"case is folded", map[string][]map[string]any{"le": {acmeCert("A.Example.COM")}},
			[]string{"a.example.com"}, []string{"a.example.com"}, map[string][]string{"le": {}}},
		{"several resolvers", map[string][]map[string]any{
			"le":   {acmeCert("a.example.com"), acmeCert("k.test")},
			"zero": {acmeCert("a.example.com"), acmeCert("b.example.com")}},
			[]string{"a.example.com", "b.example.com"}, []string{"a.example.com", "a.example.com", "b.example.com"},
			map[string][]string{"le": {"k.test"}, "zero": {}}},
		{"a name that isn't a host name is never dropped", map[string][]map[string]any{"le": {acmeCert("*.example.com"), acmeCert("a b.example.com")}},
			[]string{"*.example.com"}, []string{"*.example.com"}, map[string][]string{"le": {"a b.example.com"}}},
		{"nothing covered", map[string][]map[string]any{"le": {acmeCert("k.test")}},
			[]string{"a.example.com"}, nil, map[string][]string{"le": {"k.test"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := acmeStore(t, tt.store)
			out, dropped, err := DropCovered(in, tt.names)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(dropped, tt.dropped) && !(len(dropped) == 0 && len(tt.dropped) == 0) {
				// the resolvers are visited in map order: compare as sets
				if !sameStrings(dropped, tt.dropped) {
					t.Errorf("dropped = %v, want %v", dropped, tt.dropped)
				}
			}
			if len(tt.dropped) == 0 {
				if string(out) != string(in) {
					t.Error("nothing was covered, and the file changed")
				}
				return
			}
			if got := storeHosts(t, out); !reflect.DeepEqual(got, tt.kept) {
				t.Errorf("kept = %v, want %v", got, tt.kept)
			}
			// Everything not changed is kept: the account, the unknown members, each kept certificate's own values.
			var top map[string]map[string]json.RawMessage
			if err := json.Unmarshal(out, &top); err != nil {
				t.Fatal(err)
			}
			for name := range tt.store {
				if !strings.Contains(string(top[name]["Account"]), sentinelAccount) || !strings.Contains(string(top[name]["Extra"]), `"kept"`) {
					t.Errorf("resolver %s lost its account or an unknown member: %s", name, out)
				}
			}
			for _, hosts := range tt.kept {
				for _, h := range hosts {
					main := strings.Split(h, "+")[0]
					if !strings.Contains(string(out), sentinelCertificate+"-"+main) || !strings.Contains(string(out), sentinelKey+"-"+main) {
						t.Errorf("the kept certificate of %s lost its certificate or key", main)
					}
				}
			}
			// Idempotent: the rule over its own output drops nothing and changes nothing.
			again, droppedAgain, err := DropCovered(out, tt.names)
			if err != nil || len(droppedAgain) != 0 || string(again) != string(out) {
				t.Errorf("a second run: %v, %v, changed %v", droppedAgain, err, string(again) != string(out))
			}
		})
	}
}

func sameStrings(a, b []string) bool {
	x, y := map[string]int{}, map[string]int{}
	for _, s := range a {
		x[s]++
	}
	for _, s := range b {
		y[s]++
	}
	return reflect.DeepEqual(x, y)
}

func TestDropCoveredEmptyAndForeignStores(t *testing.T) {
	names := []string{"a.example.com"}
	for _, in := range []string{"", "   \n\t", "null", "{}", `{"le": null}`, `{"le": {}}`, `{"le": {"Certificates": null}}`, `{"le": "x"}`, `{"le": {"Certificates": []}}`} {
		out, dropped, err := DropCovered([]byte(in), names)
		if err != nil || len(dropped) != 0 || string(out) != in {
			t.Errorf("%q: out %q, dropped %v, err %v", in, out, dropped, err)
		}
	}
	for _, in := range []string{"not json", "[1,2]", `"x"`, "12", `{"le": {"Certificates": [`, `{"a.example.com": 1}x`} {
		out, dropped, err := DropCovered([]byte(in), names)
		if err == nil || out != nil || dropped != nil {
			t.Errorf("%q: out %q, dropped %v, err %v", in, out, dropped, err)
		}
		if err != nil && strings.Contains(err.Error(), in) {
			t.Errorf("the error quotes the input: %v", err)
		}
	}
	// A certificate entry that can't be read is kept, and so is one with no names.
	in := `{"le": {"Certificates": ["x", {"domain": {}}, {"domain": {"main": "a.example.com"}}]}}`
	out, dropped, err := DropCovered([]byte(in), names)
	if err != nil || !reflect.DeepEqual(dropped, []string{"a.example.com"}) || !strings.Contains(string(out), `"x"`) {
		t.Errorf("odd entries: %s, %v, %v", out, dropped, err)
	}
}

func TestValidDropName(t *testing.T) {
	ok := []string{"a.example.com", "*.example.com", "a-b.c.io", "xn--bcher-kva.example", "1.2.3.4", strings.Repeat("a", 63) + ".com", "a.b"}
	bad := []string{"", "example", "*.com", "*", "*.*.example.com", "A.example.com", "a..example.com", ".example.com", "example.com.", "-a.example.com",
		"a-.example.com", "a_b.example.com", "a b.example.com", "a.example.com/x", strings.Repeat("a", 64) + ".com",
		strings.Repeat("a.", 127) + "com", "*.a", "a.example.com\n", "ünï.example.com"}
	for _, n := range ok {
		if !ValidDropName(n) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range bad {
		if ValidDropName(n) {
			t.Errorf("%q should be refused", n)
		}
	}
}

// storeFolder is an agent with Traefik's folder under its root.
func storeFolder(t *testing.T, files map[string][]byte) (*testAgent, string) {
	t.Helper()
	a := newTestAgent(t, newConsole(t))
	dir := a.file("var/lib/caravana-host/traefik")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return a, dir
}

func TestDropCoveredCertificatesWalksTheFolder(t *testing.T) {
	covered := acmeStore(t, map[string][]map[string]any{"le": {acmeCert("a.example.com"), acmeCert("other.test")}})
	other := acmeStore(t, map[string][]map[string]any{"le": {acmeCert("b.example.com")}})
	untouched := acmeStore(t, map[string][]map[string]any{"le": {acmeCert("k.test")}})
	a, dir := storeFolder(t, map[string][]byte{
		"acme.json":              covered,
		"acme-0123456789ab.json": other,
		"acme-ABCDEFABCDEF.json": covered, // uppercase hex
		"acme-abc.json":          covered,
		"acme.json.bak":          covered,
		"certs.json":             covered,
		"acme-aaaaaaaaaaaa.json": untouched,
	})
	// A symlink named like a store file, and a folder named like one, are left alone.
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, covered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "acme-bbbbbbbbbbbb.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "acme-cccccccccccc.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "acme.json"), 0o640); err != nil {
		t.Fatal(err)
	}
	before := a.snapshot(t)

	// A dry run says what it would drop and changes nothing.
	dropped, err := a.DropCoveredCertificates([]string{"a.example.com", "b.example.com"}, true)
	if err != nil || !sameStrings(dropped, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("dry run: %v, %v", dropped, err)
	}
	if !sameSnapshot(before, a.snapshot(t)) {
		t.Fatal("a dry run changed a file")
	}

	dropped, err = a.DropCoveredCertificates([]string{"a.example.com", "b.example.com"}, false)
	if err != nil || !sameStrings(dropped, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("dropped = %v, %v", dropped, err)
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if got := storeHosts(t, read("acme.json")); !reflect.DeepEqual(got, map[string][]string{"le": {"other.test"}}) {
		t.Errorf("acme.json = %v", got)
	}
	if got := storeHosts(t, read("acme-0123456789ab.json")); !reflect.DeepEqual(got, map[string][]string{"le": {}}) {
		t.Errorf("acme-0123456789ab.json = %v", got)
	}
	for _, name := range []string{"acme-ABCDEFABCDEF.json", "acme-abc.json", "acme.json.bak", "certs.json", "acme-aaaaaaaaaaaa.json"} {
		if string(read(name)) != map[bool]string{true: string(covered), false: string(untouched)}[name != "acme-aaaaaaaaaaaa.json"] {
			t.Errorf("%s changed", name)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != string(covered) {
		t.Error("the file behind a symlink changed")
	}
	if fi, err := os.Stat(filepath.Join(dir, "acme.json")); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("acme.json's mode: %v, %v", fi, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 9 {
		t.Errorf("the folder has %d entries, a temporary file was left", len(entries))
	}
	// Again: nothing left to drop.
	if dropped, err = a.DropCoveredCertificates([]string{"a.example.com", "b.example.com"}, false); err != nil || len(dropped) != 0 {
		t.Errorf("a second run: %v, %v", dropped, err)
	}
}

func TestDropCoveredCertificatesWithoutAFolderOrAGoodFile(t *testing.T) {
	a := newTestAgent(t, newConsole(t))
	if a.HasTraefikStore() {
		t.Error("no folder, yet the store is here")
	}
	if dropped, err := a.DropCoveredCertificates([]string{"a.example.com"}, false); err != nil || len(dropped) != 0 {
		t.Errorf("no folder: %v, %v", dropped, err)
	}

	good := acmeStore(t, map[string][]map[string]any{"le": {acmeCert("a.example.com")}})
	a, dir := storeFolder(t, map[string][]byte{"acme-0123456789ab.json": good, "acme.json": []byte("this is not json " + sentinelKey)})
	if !a.HasTraefikStore() {
		t.Error("the folder is there, yet no store")
	}
	before := a.snapshot(t)
	_, err := a.DropCoveredCertificates([]string{"a.example.com"}, false)
	var sf *storeFileError
	if err == nil || !errorsAs(err, &sf) || sf.base != "acme.json" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), sentinelKey) || strings.Contains(err.Error(), "this is not json") {
		t.Errorf("the error quotes the file: %v", err)
	}
	if !sameSnapshot(before, a.snapshot(t)) {
		t.Error("a file was written although another wasn't a store")
	}

	// A file over 32 MiB is refused without being read.
	big := filepath.Join(dir, "acme.json")
	f, err := os.OpenFile(big, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxStoreFile + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := a.DropCoveredCertificates([]string{"a.example.com"}, false); err == nil || !strings.Contains(err.Error(), "acme.json is larger than 32 MiB") {
		t.Errorf("a big file: %v", err)
	}
}

func errorsAs(err error, target **storeFileError) bool {
	for err != nil {
		if e, ok := err.(*storeFileError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// dropAgent is an agent with state, a Traefik folder holding one store with a certificate for
// a.example.com and one for other.test.
func dropAgent(t *testing.T) (*testAgent, State) {
	t.Helper()
	a, _ := storeFolder(t, map[string][]byte{"acme.json": acmeStore(t, map[string][]map[string]any{"le": {acmeCert("a.example.com"), acmeCert("other.test")}})})
	st := State{Index: 1, Name: "fsn-1", Pool: "default", Role: RoleServer, First: true, Servers: []int{1}}
	if err := a.SaveState(st); err != nil {
		t.Fatal(err)
	}
	return a, st
}

func (a *testAgent) storeNow(t *testing.T) map[string][]string {
	t.Helper()
	return storeHosts(t, []byte(a.read(t, "var/lib/caravana-host/traefik/acme.json")))
}

func (a *testAgent) dropOutcome(t *testing.T) *CertificateDropOutcome {
	t.Helper()
	st, err := a.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return st.CertificateDrop
}

func TestApplyCertificateDropOutcomes(t *testing.T) {
	t.Run("dropped", func(t *testing.T) {
		a, st := dropAgent(t)
		got, dropped := a.applyCertificateDrop(st, &CertificateDropRequest{ID: dropID1, Names: []string{"a.example.com"}})
		want := &CertificateDropOutcome{ID: dropID1, State: DropDropped, Dropped: []string{"a.example.com"}}
		if !dropped || !reflect.DeepEqual(got.CertificateDrop, want) || !reflect.DeepEqual(a.dropOutcome(t), want) {
			t.Fatalf("outcome = %+v (saved %+v), dropped %v", got.CertificateDrop, a.dropOutcome(t), dropped)
		}
		if h := a.storeNow(t); !reflect.DeepEqual(h, map[string][]string{"le": {"other.test"}}) {
			t.Errorf("store = %v", h)
		}
		if !strings.Contains(a.stdout.String(), "Dropped the earlier certificates of a.example.com.") {
			t.Errorf("stdout = %q", a.stdout.String())
		}
		// The same ID is not run again, even for other names.
		before := a.snapshot(t)
		again, dropped := a.applyCertificateDrop(got, &CertificateDropRequest{ID: dropID1, Names: []string{"other.test"}})
		if dropped || again.CertificateDrop != got.CertificateDrop || !sameSnapshot(before, a.snapshot(t)) {
			t.Error("an ID with a final outcome ran again")
		}
		// Another ID is.
		next, dropped := a.applyCertificateDrop(got, &CertificateDropRequest{ID: dropID2, Names: []string{"other.test"}})
		if !dropped || next.CertificateDrop.ID != dropID2 || !reflect.DeepEqual(a.storeNow(t), map[string][]string{"le": {}}) {
			t.Errorf("a new ID: %+v, %v", next.CertificateDrop, a.storeNow(t))
		}
	})
	t.Run("nothing", func(t *testing.T) {
		a, st := dropAgent(t)
		before := a.snapshot(t)
		got, dropped := a.applyCertificateDrop(st, &CertificateDropRequest{ID: dropID1, Names: []string{"nowhere.example.org"}})
		if dropped || got.CertificateDrop == nil || got.CertificateDrop.State != DropNothing || len(got.CertificateDrop.Dropped) != 0 {
			t.Fatalf("outcome = %+v, %v", got.CertificateDrop, dropped)
		}
		snap := a.snapshot(t)
		delete(snap, "/etc/caravana-node/state.json")
		delete(before, "/etc/caravana-node/state.json")
		if !sameSnapshot(before, snap) {
			t.Error("nothing was covered, and a file changed")
		}
	})
	t.Run("failed", func(t *testing.T) {
		a, st := dropAgent(t)
		if err := os.WriteFile(a.file("var/lib/caravana-host/traefik/acme-0123456789ab.json"), []byte("garbage "+sentinelKey), 0o600); err != nil {
			t.Fatal(err)
		}
		got, dropped := a.applyCertificateDrop(st, &CertificateDropRequest{ID: dropID1, Names: []string{"a.example.com"}})
		o := got.CertificateDrop
		if dropped || o == nil || o.State != DropFailed || len(o.Dropped) != 0 ||
			o.Reason != "Traefik's certificate store couldn't be changed: acme-0123456789ab.json isn't a Traefik certificate store." {
			t.Fatalf("outcome = %+v", o)
		}
		if h := a.storeNow(t); len(h["le"]) != 2 {
			t.Errorf("a store file changed although another failed: %v", h)
		}
	})
	t.Run("refused names", func(t *testing.T) {
		a, st := dropAgent(t)
		before := a.snapshot(t)
		for _, names := range [][]string{nil, {}, {"*"}, {"A.example.com"}, {"a.example.com", "../x"}, make([]string, 1001)} {
			got, dropped := a.applyCertificateDrop(st, &CertificateDropRequest{ID: dropID1, Names: names})
			if dropped || got.CertificateDrop == nil || got.CertificateDrop.State != DropRefused ||
				got.CertificateDrop.Reason != "The names aren't host names this agent accepts." {
				t.Errorf("names %v: %+v", names, got.CertificateDrop)
			}
		}
		snap := a.snapshot(t)
		delete(snap, "/etc/caravana-node/state.json")
		delete(before, "/etc/caravana-node/state.json")
		if !sameSnapshot(before, snap) {
			t.Error("refused names changed a file")
		}
	})
	t.Run("refused without the store", func(t *testing.T) {
		a := newTestAgent(t, newConsole(t))
		got, dropped := a.applyCertificateDrop(State{Index: 1, Name: "fsn-1", Pool: "default", Role: RoleServer}, &CertificateDropRequest{ID: dropID1, Names: []string{"a.example.com"}})
		if dropped || got.CertificateDrop == nil || got.CertificateDrop.State != DropRefused ||
			got.CertificateDrop.Reason != "This server doesn't hold Traefik's certificate store." {
			t.Errorf("outcome = %+v", got.CertificateDrop)
		}
	})
	t.Run("a bad ID is ignored", func(t *testing.T) {
		a, st := dropAgent(t)
		for _, id := range []string{"", "xyz", strings.ToUpper(dropID1), dropID1 + "0"} {
			got, dropped := a.applyCertificateDrop(st, &CertificateDropRequest{ID: id, Names: []string{"a.example.com"}})
			if dropped || got.CertificateDrop != nil || a.dropOutcome(t) != nil {
				t.Errorf("id %q recorded something", id)
			}
		}
		if !strings.Contains(a.stderr.String(), "isn't 32 hex digits. Ignored.") || len(a.storeNow(t)["le"]) != 2 {
			t.Errorf("stderr %q, store %v", a.stderr.String(), a.storeNow(t))
		}
	})
	t.Run("no request is no step", func(t *testing.T) {
		a, st := dropAgent(t)
		got, dropped := a.applyCertificateDrop(st, nil)
		if dropped || got.CertificateDrop != nil {
			t.Error("a nil request did something")
		}
	})
	t.Run("a run found running goes again", func(t *testing.T) {
		a, st := dropAgent(t)
		st.CertificateDrop = &CertificateDropOutcome{ID: dropID1, State: DropRunning}
		if err := a.SaveState(st); err != nil {
			t.Fatal(err)
		}
		got, dropped := a.applyCertificateDrop(st, &CertificateDropRequest{ID: dropID1, Names: []string{"a.example.com"}})
		if !dropped || got.CertificateDrop.State != DropDropped {
			t.Errorf("outcome = %+v", got.CertificateDrop)
		}
	})
	t.Run("a run found running with nothing left still says dropped", func(t *testing.T) {
		// The files were rewritten before the agent stopped: Traefik must still be restarted.
		a, st := dropAgent(t)
		if _, err := a.DropCoveredCertificates([]string{"a.example.com"}, false); err != nil {
			t.Fatal(err)
		}
		st.CertificateDrop = &CertificateDropOutcome{ID: dropID1, State: DropRunning}
		if err := a.SaveState(st); err != nil {
			t.Fatal(err)
		}
		got, dropped := a.applyCertificateDrop(st, &CertificateDropRequest{ID: dropID1, Names: []string{"a.example.com"}})
		if !dropped || got.CertificateDrop.State != DropDropped || len(got.CertificateDrop.Dropped) != 0 {
			t.Errorf("outcome = %+v, dropped %v", got.CertificateDrop, dropped)
		}
	})
	t.Run("the state must be writable", func(t *testing.T) {
		a, st := dropAgent(t)
		if err := os.Remove(a.file("etc/caravana-node/state.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(a.file("etc/caravana-node/state.json/x"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, dropped := a.applyCertificateDrop(st, &CertificateDropRequest{ID: dropID1, Names: []string{"a.example.com"}})
		if dropped || got.CertificateDrop != nil || len(a.storeNow(t)["le"]) != 2 {
			t.Errorf("something was done with no way to record it: %+v, %v", got.CertificateDrop, a.storeNow(t))
		}
	})
}

func TestStateValidatesTheCertificateDropOutcome(t *testing.T) {
	st := State{Version: stateVersion, Index: 1, Name: "fsn-1", Pool: "default", Role: RoleServer}
	for name, o := range map[string]*CertificateDropOutcome{
		"none":      nil,
		"running":   {ID: dropID1, State: DropRunning},
		"dropped":   {ID: dropID1, State: DropDropped, Dropped: []string{"a.example.com"}},
		"nothing":   {ID: dropID1, State: DropNothing},
		"refused":   {ID: dropID1, State: DropRefused, Reason: "x"},
		"failed":    {ID: dropID1, State: DropFailed, Reason: "x"},
		"bad id":    {ID: "x", State: DropDropped},
		"bad state": {ID: dropID1, State: "done"},
		"too many":  {ID: dropID1, State: DropDropped, Dropped: make([]string, 101)},
	} {
		st.CertificateDrop = o
		wantErr := strings.HasPrefix(name, "bad") || name == "too many"
		if err := st.Validate(); (err != nil) != wantErr {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// syncDrop makes a joined agent with a store whose console answers every sync with the request.
func syncDrop(t *testing.T, req map[string]any) *testAgent {
	t.Helper()
	a, _ := storeFolder(t, map[string][]byte{"acme.json": acmeStore(t, map[string][]map[string]any{"le": {acmeCert("a.example.com"), acmeCert("other.test")}})})
	a.console.serveJoin()
	a.joined(t)
	a.console.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		if req != nil {
			m["certificateDrop"] = req
		}
	}))
	return a
}

func TestSyncDropsAndReportsAgainAtOnce(t *testing.T) {
	a := syncDrop(t, map[string]any{"id": dropID1, "names": []string{"a.example.com", "*.example.org"}})
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatalf("Sync: %v\n%s", err, a.stderr.String())
	}
	reqs := a.console.calls("/v1/node/sync")
	if len(reqs) != 2 {
		t.Fatalf("sync calls = %d, want the report and one more", len(reqs))
	}
	first, second := reqs[0].body, reqs[1].body
	if first["traefikStore"] != true || first["certificateDrop"] != nil {
		t.Errorf("first report: traefikStore %v, certificateDrop %v", first["traefikStore"], first["certificateDrop"])
	}
	got, _ := second["certificateDrop"].(map[string]any)
	if got["id"] != dropID1 || got["state"] != "dropped" || !reflect.DeepEqual(got["dropped"], []any{"a.example.com"}) {
		t.Errorf("second report's certificateDrop = %v", got)
	}
	if second["traefikStore"] != true || reqs[1].auth != "Bearer "+sentinelNodeToken {
		t.Errorf("second report: %v, auth %q", second["traefikStore"], reqs[1].auth)
	}
	if h := a.storeNow(t); !reflect.DeepEqual(h, map[string][]string{"le": {"other.test"}}) {
		t.Errorf("store = %v", h)
	}

	// The next sync repeats the console's request and posts once: the outcome is kept, nothing runs again.
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(a.console.calls("/v1/node/sync")); n != 3 {
		t.Errorf("sync calls = %d after the second sync, want 3", n)
	}
	if reqs = a.console.calls("/v1/node/sync"); reqs[2].body["certificateDrop"] == nil {
		t.Error("the outcome wasn't reported again at the next sync")
	}

	// No certificate, key or account of the store anywhere the console or the log could see.
	leaks := []string{sentinelCertificate, sentinelKey, sentinelAccount, "ana@example.com"}
	surfaces := map[string]string{"stdout": a.stdout.String(), "stderr": a.stderr.String(), "state.json": a.read(t, "etc/caravana-node/state.json")}
	for i, r := range a.console.calls("/v1/node/sync") {
		b, _ := json.Marshal(r.body)
		surfaces["request "+string(rune('0'+i))] = string(b)
	}
	for name, text := range surfaces {
		for _, leak := range leaks {
			if strings.Contains(text, leak) {
				t.Errorf("%s holds %q", name, leak)
			}
		}
	}
}

func TestSyncDropsNothingAndPostsOnce(t *testing.T) {
	a := syncDrop(t, map[string]any{"id": dropID1, "names": []string{"nowhere.example.org"}})
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(a.console.calls("/v1/node/sync")); n != 1 {
		t.Errorf("sync calls = %d: nothing was dropped, so no second report", n)
	}
	if o := a.dropOutcome(t); o == nil || o.State != DropNothing {
		t.Errorf("outcome = %+v", o)
	}
}

func TestSyncWithoutAStoreRefusesAndGoesOn(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["certificateDrop"] = map[string]any{"id": dropID1, "names": []string{"a.example.com"}}
		m["renew"] = false
	}))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if o := a.dropOutcome(t); o == nil || o.State != DropRefused {
		t.Errorf("outcome = %+v", o)
	}
	if first := c.calls("/v1/node/sync")[0].body; first["traefikStore"] != nil {
		t.Errorf("a server with no store says traefikStore = %v", first["traefikStore"])
	}
}

func TestSyncSecondReportFailingChangesNothing(t *testing.T) {
	a := syncDrop(t, map[string]any{"id": dropID1, "names": []string{"a.example.com"}})
	calls := 0
	a.console.on("/v1/node/sync", func(req map[string]any, auth string) (int, any) {
		calls++
		if calls > 1 {
			return http.StatusInternalServerError, nil
		}
		return a.syncAnswer(func(m map[string]any) {
			m["certificateDrop"] = map[string]any{"id": dropID1, "names": []string{"a.example.com"}}
		})(req, auth)
	})
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatalf("a failed second report failed the sync: %v", err)
	}
	if !strings.Contains(a.stderr.String(), "second report of the dropped certificates") {
		t.Errorf("stderr = %q", a.stderr.String())
	}
	if o := a.dropOutcome(t); o == nil || o.State != DropDropped {
		t.Errorf("outcome = %+v", o)
	}
}

func TestSyncIgnoresABadDropID(t *testing.T) {
	a := syncDrop(t, map[string]any{"id": "nope", "names": []string{"a.example.com"}})
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if a.dropOutcome(t) != nil || len(a.console.calls("/v1/node/sync")) != 1 || len(a.storeNow(t)["le"]) != 2 {
		t.Error("a request with a bad ID did something")
	}
}

// The step never keeps the clean-up, which comes after it, from running.
func TestCertificateDropDoesNotBlockTheCleanUp(t *testing.T) {
	a := syncDrop(t, nil)
	d := newFakeDocker(t)
	withDocker(a, d)
	a.console.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["certificateDrop"] = map[string]any{"id": dropID1, "names": []string{"a.example.com"}}
		m["clean"] = map[string]any{"id": cleanIDA, "keep": []string{}, "dockerVolumes": false}
	}))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := a.LoadState()
	if st.CertificateDrop == nil || st.CertificateDrop.State != DropDropped || st.LastClean == nil {
		t.Errorf("state lost something: drop %+v, clean %+v", st.CertificateDrop, st.LastClean)
	}
}
