package service

import (
	"context"
	"errors"
	"testing"
)

// The console moves to another domain (feature 051) and the owner's passkeys stay with the old one: a
// passkey belongs to its domain, so none works at the new address. Password sign-in is off, because the
// owner turned it off at the old address. Nobody may be locked out: 033's rule keeps the password working
// at a domain where no passkey exists, and turns it off again once the owner adds one there.
func TestPasswordSignInAtTheNewConsoleDomain(t *testing.T) {
	ctx := context.Background()
	rpNew := RelyingParty{ID: "new.example.com", Origin: "https://new.example.com"}

	e := newAuthEnvWithAdmin(t)
	session := signedIn(t, e)
	addPasskey(t, e, session, rpOther, "At the old domain") // old.example.com is where the console was
	if _, err := e.auth.SetPasswordSignIn(ctx, false, rpOther.ID); err != nil {
		t.Fatal(err)
	}

	// At the old address the password is refused, as before the move.
	if _, err := e.auth.SignIn(ctx, testEmail, testPassword, rpOther.ID, addrA); !errors.Is(err, ErrPasswordSignInOff) {
		t.Errorf("SignIn at the old domain = %v, want ErrPasswordSignInOff", err)
	}
	// At the new address, with no passkey there, it works: the session cookie is per host, so the owner
	// signs in again, and this is how.
	if ok, err := e.auth.PasswordSignInAllowed(ctx, rpNew.ID); err != nil || !ok {
		t.Fatalf("PasswordSignInAllowed at the new domain = %v, %v; want true", ok, err)
	}
	atNew, err := e.auth.SignIn(ctx, testEmail, testPassword, rpNew.ID, addrA)
	if err != nil {
		t.Fatalf("SignIn at the new domain = %v, want it to work", err)
	}
	old := unconfirmedSession(t, e, "session-from-before-the-move")
	if err := e.auth.VerifyPassword(ctx, old, testPassword, rpNew.ID, addrA); err != nil {
		t.Errorf("confirming with the password at the new domain = %v, want it to work", err)
	}

	// The owner adds a passkey at the new address: from then on the password is off there too.
	addPasskey(t, e, atNew.Value, rpNew, "At the new domain")
	if _, err := e.auth.SignIn(ctx, testEmail, testPassword, rpNew.ID, addrA); !errors.Is(err, ErrPasswordSignInOff) {
		t.Errorf("SignIn at the new domain once it has a passkey = %v, want ErrPasswordSignInOff", err)
	}
}
