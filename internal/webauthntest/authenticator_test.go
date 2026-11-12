package webauthntest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// user is the one account of these tests, for go-webauthn.
type user struct {
	id          []byte
	credentials []webauthn.Credential
}

func (u *user) WebAuthnID() []byte                         { return u.id }
func (u *user) WebAuthnName() string                       { return "you@example.com" }
func (u *user) WebAuthnDisplayName() string                { return "you@example.com" }
func (u *user) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func relyingParty(t *testing.T, rpID, origin string) *webauthn.WebAuthn {
	t.Helper()
	w, err := webauthn.New(&webauthn.Config{RPID: rpID, RPDisplayName: "caravana", RPOrigins: []string{origin}})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// register runs a whole registration against go-webauthn and returns the credential it accepted.
func register(t *testing.T, w *webauthn.WebAuthn, u *user, a *Authenticator, origin string) *webauthn.Credential {
	t.Helper()
	creation, session, err := w.BeginRegistration(u,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation))
	if err != nil {
		t.Fatal(err)
	}
	options, _ := json.Marshal(creation.Response)
	posted, err := a.Create(options, origin)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(posted)
	if err != nil {
		t.Fatalf("parse creation: %v", err)
	}
	cred, err := w.CreateCredential(u, *session, parsed)
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	return cred
}

// signIn runs a discoverable sign-in. The error is the library's verdict on what Get made.
func signIn(t *testing.T, w *webauthn.WebAuthn, u *user, a *Authenticator, origin string) (*webauthn.Credential, error) {
	t.Helper()
	assertion, session, err := w.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		t.Fatal(err)
	}
	options, _ := json.Marshal(assertion.Response)
	posted, err := a.Get(options, origin)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(posted)
	if err != nil {
		return nil, err
	}
	_, cred, err := w.ValidatePasskeyLogin(func(rawID, handle []byte) (webauthn.User, error) { return u, nil }, *session, parsed)
	return cred, err
}

func TestGoWebauthnAcceptsWhatItMakes(t *testing.T) {
	for _, tc := range []struct{ name, rpID, origin string }{
		{"https domain", "console.example.com", "https://console.example.com"},
		{"localhost with a port", "localhost", "http://localhost:7070"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := relyingParty(t, tc.rpID, tc.origin)
			u := &user{id: []byte("0123456789abcdef0123456789abcdef")}
			a := New(tc.rpID)

			cred := register(t, w, u, a, tc.origin)
			if string(cred.ID) != string(a.CredentialID()) {
				t.Error("the stored credential ID is not the authenticator's")
			}
			if !cred.Flags.UserVerified || cred.Authenticator.SignCount != 0 {
				t.Errorf("registered flags = %+v, sign count = %d; want UV and 0", cred.Flags, cred.Authenticator.SignCount)
			}
			if string(a.UserHandle) != string(u.id) {
				t.Error("Create did not take the user handle from the options")
			}
			u.credentials = []webauthn.Credential{*cred}

			// Two sign-ins, counting 1 then 2.
			for want := uint32(1); want <= 2; want++ {
				got, err := signIn(t, w, u, a, tc.origin)
				if err != nil {
					t.Fatalf("sign-in %d: %v", want, err)
				}
				if got.Authenticator.SignCount != want || got.Authenticator.CloneWarning {
					t.Errorf("sign-in %d: count = %d, clone warning = %v", want, got.Authenticator.SignCount, got.Authenticator.CloneWarning)
				}
				u.credentials = []webauthn.Credential{*got}
			}
		})
	}
}

func TestKnobs(t *testing.T) {
	const rp, origin = "console.example.com", "https://console.example.com"
	w := relyingParty(t, rp, origin)
	u := &user{id: []byte("0123456789abcdef0123456789abcdef")}
	a := New(rp)
	u.credentials = []webauthn.Credential{*register(t, w, u, a, origin)}

	t.Run("a repeated counter is a clone warning", func(t *testing.T) {
		a.Counter = 5
		got, err := signIn(t, w, u, a, origin)
		if err != nil {
			t.Fatal(err)
		}
		u.credentials = []webauthn.Credential{*got}
		a.Counter = 5 // the same again
		got, err = signIn(t, w, u, a, origin)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Authenticator.CloneWarning {
			t.Error("a repeated sign count did not raise the clone warning")
		}
	})

	t.Run("without user verification", func(t *testing.T) {
		b := *a
		b.UserVerified = false
		if _, err := signIn(t, w, u, &b, origin); err == nil {
			t.Error("an assertion without UV was accepted although UV was required")
		}
	})

	t.Run("another origin", func(t *testing.T) {
		if _, err := signIn(t, w, u, a, "https://evil.example.net"); err == nil {
			t.Error("an assertion made at another origin was accepted")
		}
	})

	t.Run("another RP ID", func(t *testing.T) {
		b := *a
		b.RPID = "old.example.com"
		if _, err := signIn(t, w, u, &b, origin); err == nil {
			t.Error("an assertion signed for another RP ID was accepted")
		}
	})

	t.Run("another user handle is passed on", func(t *testing.T) {
		b := *a
		b.UserHandle = []byte("someone else entirely, 32 bytes!")
		assertion, _, _ := w.BeginDiscoverableLogin()
		options, _ := json.Marshal(assertion.Response)
		posted, err := b.Get(options, origin)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := protocol.ParseCredentialRequestResponseBytes(posted)
		if err != nil || string(parsed.Response.UserHandle) != "someone else entirely, 32 bytes!" {
			t.Errorf("parsed user handle = %q, %v", parsed.Response.UserHandle, err)
		}
	})

	t.Run("options wrapped in publicKey", func(t *testing.T) {
		assertion, _, _ := w.BeginDiscoverableLogin()
		wrapped, _ := json.Marshal(assertion) // {"publicKey": {...}}
		if _, err := a.Get(wrapped, origin); err != nil {
			t.Errorf("Get with {\"publicKey\": ...}: %v", err)
		}
	})
}

func TestPrivateKeySentinel(t *testing.T) {
	a, b := New("a.example.com"), New("a.example.com")
	forms := a.PrivateKeySentinel()
	if len(forms) != 3 {
		t.Fatalf("PrivateKeySentinel = %v, want hex, base64 and base64url", forms)
	}
	for _, f := range forms {
		if len(f) < 40 {
			t.Errorf("sentinel %q is too short to search for", f)
		}
	}
	if forms[0] == b.PrivateKeySentinel()[0] {
		t.Error("two authenticators share a private key")
	}

	// What the authenticator posts holds none of them.
	w := relyingParty(t, "a.example.com", "https://a.example.com")
	u := &user{id: []byte("0123456789abcdef0123456789abcdef")}
	creation, _, _ := w.BeginRegistration(u)
	options, _ := json.Marshal(creation.Response)
	posted, err := a.Create(options, "https://a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range forms {
		if strings.Contains(string(posted), f) {
			t.Errorf("the registration JSON holds the private key as %q", f[:8])
		}
	}
}
