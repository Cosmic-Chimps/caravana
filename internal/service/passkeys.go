package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Passkeys (feature 033). The admin may sign in with a WebAuthn passkey instead of the password,
// and may turn the password off. This file is the ceremonies (begin and finish for signing in,
// confirming it is them and adding a passkey), the passkey list and the password-sign-in setting.
// go-webauthn does the parsing and the checks of what an authenticator sends; what Caravana adds
// is who may do it, from where, how often and what is kept. See specs/033-passkeys/plan.md, R1-R13.

const (
	// confirmWindow is how long after signing in, or after "Confirm it's you", a
	// session may add a passkey (R6).
	confirmWindow = 10 * time.Minute

	maxPasskeyName     = 64
	passkeyHandleBytes = 32       // the admin's WebAuthn user handle
	maxCredentialBytes = 64 << 10 // a posted credential is a few KB; more is refused unread
)

// Errors callers can check with errors.Is.
var (
	// ErrPasskeyRefused is any passkey sign-in or confirmation that failed: an unknown or used
	// ceremony, an unknown credential, another domain, a bad signature, a missing user
	// verification, a sign count that went backwards. It never says which: the page says the
	// passkey didn't work here, and the log has a reason word.
	ErrPasskeyRefused = errors.New("that passkey didn't work here")
	// ErrNotConfirmed is adding a passkey from a session that isn't confirmed (confirmWindow).
	ErrNotConfirmed = errors.New("confirm it's you first")
	// ErrLastPasskey is removing the last passkey of a domain while password sign-in is off.
	ErrLastPasskey = errors.New("this is the last passkey and password sign-in is off")
	// ErrNoPasskey is turning password sign-in off with no passkey for the console's domain.
	ErrNoPasskey = errors.New("there is no passkey for this domain")
	// ErrPasskeyNotFound is an ID of ours that names no passkey.
	ErrPasskeyNotFound = errors.New("passkey not found")
)

// PasskeyFailure says how many tries are left after a passkey sign-in or confirmation that
// failed. It wraps ErrPasskeyRefused.
type PasskeyFailure struct{ TriesLeft int }

func (e *PasskeyFailure) Error() string { return ErrPasskeyRefused.Error() }
func (e *PasskeyFailure) Unwrap() error { return ErrPasskeyRefused }

// PasskeyDomainError is ErrLastPasskey or ErrNoPasskey with the domain it is about, in the words
// the API sends. Domain is "" when the console's domain isn't known (any passkey counts).
type PasskeyDomainError struct {
	Err    error // ErrLastPasskey or ErrNoPasskey
	Domain string
}

func (e *PasskeyDomainError) Unwrap() error { return e.Err }

func (e *PasskeyDomainError) Error() string {
	where := "this console's domain"
	if e.Domain != "" {
		where = e.Domain
	}
	if errors.Is(e.Err, ErrLastPasskey) {
		return fmt.Sprintf("this is the last passkey for %s and password sign-in is off: turn password sign-in on first, or add another passkey", where)
	}
	return fmt.Sprintf("add a passkey for %s before turning password sign-in off", where)
}

// Passkey is a passkey as a list shows it. It never holds the credential ID or the public key.
type Passkey struct {
	ID         string // ours, not the authenticator's
	Name       string
	Domain     string // the RP ID it was made at
	CreatedAt  time.Time
	LastUsedAt *time.Time
	Transports []string
	BackedUp   bool // the authenticator says it is synced between devices
}

// PasskeyOptions is what a begin answers: the ceremony to send back with the finish and the
// publicKey object for navigator.credentials.create() or .get(), as JSON.
type PasskeyOptions struct {
	Ceremony string
	Options  json.RawMessage
}

// SignInSettings is how the admin may sign in.
type SignInSettings struct {
	PasswordSignIn bool
	Passkeys       int // all of them, whatever domain they were made at
}

func passkeyOf(r store.PasskeyRecord) Passkey {
	return Passkey{
		ID: r.ID, Name: r.Name, Domain: r.RPID, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt,
		Transports: append([]string{}, r.Transports...), BackedUp: r.BackupState,
	}
}

// passkeyUser is the admin as go-webauthn wants a user: the handle, a name and the credentials
// the ceremony may use.
type passkeyUser struct {
	handle      []byte
	email       string
	credentials []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u *passkeyUser) WebAuthnName() string                       { return u.email }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.email }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// credentialOf is a stored passkey as go-webauthn's Credential.
func credentialOf(r store.PasskeyRecord) webauthn.Credential {
	var flags protocol.AuthenticatorFlags
	for flag, set := range map[protocol.AuthenticatorFlags]bool{
		protocol.FlagUserPresent:    r.UserPresent,
		protocol.FlagUserVerified:   r.UserVerified,
		protocol.FlagBackupEligible: r.BackupEligible,
		protocol.FlagBackupState:    r.BackupState,
	} {
		if set {
			flags |= flag
		}
	}
	transports := make([]protocol.AuthenticatorTransport, len(r.Transports))
	for i, t := range r.Transports {
		transports[i] = protocol.AuthenticatorTransport(t)
	}
	return webauthn.Credential{
		ID: r.CredentialID, PublicKey: r.PublicKey,
		AttestationType: r.AttestationType, AttestationFormat: r.AttestationFormat,
		Transport: transports, Flags: webauthn.NewCredentialFlags(flags),
		Authenticator: webauthn.Authenticator{AAGUID: r.AAGUID, SignCount: r.SignCount},
	}
}

// webauthnFor is a go-webauthn relying party for rp (R1, R8): user verification and a resident
// key are required, attestation is not asked for, and the browser is told to give up after two
// minutes. Caravana keeps its own expiry (ceremonyLife), so the library enforces none.
func webauthnFor(rp RelyingParty) (*webauthn.WebAuthn, error) {
	const timeout = 2 * time.Minute
	w, err := webauthn.New(&webauthn.Config{
		RPID: rp.ID, RPDisplayName: "caravana", RPOrigins: []string{rp.Origin},
		AttestationPreference:  protocol.PreferNoAttestation,
		AuthenticatorSelection: passkeySelection(),
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Timeout: timeout, TimeoutUVD: timeout},
			Registration: webauthn.TimeoutConfig{Timeout: timeout, TimeoutUVD: timeout},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("passkey relying party %q: %w", rp.ID, err)
	}
	return w, nil
}

func passkeySelection() protocol.AuthenticatorSelection {
	return protocol.AuthenticatorSelection{
		RequireResidentKey: new(true),
		ResidentKey:        protocol.ResidentKeyRequirementRequired,
		UserVerification:   protocol.VerificationRequired,
	}
}

// optionsJSON is the publicKey object of a begin, as JSON.
func optionsJSON(v any) (json.RawMessage, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode passkey options: %w", err)
	}
	return out, nil
}

// --- Signing in -----------------------------------------------------------------------------

// BeginPasskeySignIn starts a sign-in with a discoverable passkey: the browser offers the
// passkeys it holds for rp, so no email is typed. Asking is not counted against the address
// (nothing is guessed yet); only a finish that fails is.
func (a *Auth) BeginPasskeySignIn(ctx context.Context, rp RelyingParty, address string) (PasskeyOptions, error) {
	if _, err := a.admin(ctx); err != nil {
		return PasskeyOptions{}, err
	}
	w, err := webauthnFor(rp)
	if err != nil {
		return PasskeyOptions{}, err
	}
	assertion, session, err := w.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return PasskeyOptions{}, fmt.Errorf("begin passkey sign-in: %w", err)
	}
	return a.keepCeremony(ceremony{kind: ceremonySignIn, session: *session, rp: rp, address: address}, assertion.Response)
}

// keepCeremony stores c and answers with its ID and the options.
func (a *Auth) keepCeremony(c ceremony, options any) (PasskeyOptions, error) {
	body, err := optionsJSON(options)
	if err != nil {
		return PasskeyOptions{}, err
	}
	id, err := a.ceremonies.add(a.now().UTC(), c)
	if err != nil {
		return PasskeyOptions{}, err
	}
	return PasskeyOptions{Ceremony: id, Options: body}, nil
}

// FinishPasskeySignIn checks the browser's answer and starts a session, as SignIn does for a
// password: a locked address is refused before anything is parsed, and every failure counts
// against the address (R11). A failure is a *PasskeyFailure with the tries left, or a
// *SigninLimitError. credential is the PublicKeyCredential JSON the script posted.
func (a *Auth) FinishPasskeySignIn(ctx context.Context, rp RelyingParty, ceremonyID string, credential []byte, address string) (Session, error) {
	admin, err := a.admin(ctx)
	if err != nil {
		return Session{}, err
	}
	at, err := a.beginAttempt(ctx, address)
	if err != nil {
		var limited *SigninLimitError
		if errors.As(err, &limited) {
			a.event("signin_limited", "address", address)
		}
		return Session{}, err
	}
	defer at.release()
	fail := func(reason string) (Session, error) {
		return Session{}, a.failPasskey(ctx, at, "passkey_signin_failed", address, reason)
	}

	c, ok := a.ceremonies.take(a.now().UTC(), ceremonyID)
	if !ok || c.kind != ceremonySignIn || c.rp != rp {
		return fail("ceremony")
	}
	if len(credential) > maxCredentialBytes {
		return fail("credential")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		return fail("credential")
	}
	w, err := webauthnFor(c.rp)
	if err != nil {
		return Session{}, err
	}

	// The library asks who owns the credential once it has read the answer. The checks that
	// are ours happen here, before it verifies the signature: the user handle must be the
	// admin's (R9) and the credential must have been made at this domain.
	var (
		found    store.PasskeyRecord
		reason   string
		storeErr error
	)
	lookup := func(rawID, userHandle []byte) (webauthn.User, error) {
		if len(admin.WebAuthnHandle) == 0 || !bytes.Equal(userHandle, admin.WebAuthnHandle) {
			reason = "credential"
			return nil, errors.New("not the admin's passkey")
		}
		rec, err := a.store.GetPasskeyByCredential(ctx, rawID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			reason = "credential"
			return nil, errors.New("unknown passkey")
		case err != nil:
			storeErr = err
			return nil, errors.New("store failed")
		case rec.RPID != c.rp.ID:
			reason = "domain"
			return nil, errors.New("made for another domain")
		case !bytes.Equal(rec.UserHandle, userHandle):
			reason = "credential"
			return nil, errors.New("not the admin's passkey")
		}
		found = rec
		return &passkeyUser{handle: rec.UserHandle, email: admin.Email, credentials: []webauthn.Credential{credentialOf(rec)}}, nil
	}
	_, cred, err := w.ValidatePasskeyLogin(lookup, c.session, parsed)
	if storeErr != nil {
		return Session{}, fmt.Errorf("sign in with a passkey: %w", storeErr)
	}
	if err != nil {
		if reason == "" {
			reason = "verification"
		}
		return fail(reason)
	}
	if cred.Authenticator.CloneWarning {
		return fail("counter")
	}

	if err := a.recordPasskeyUse(ctx, found.ID, cred); err != nil {
		return Session{}, err
	}
	if err := a.clearFailures(ctx, at); err != nil {
		return Session{}, err
	}
	s, err := a.startSession(ctx)
	if err != nil {
		return Session{}, err
	}
	a.event("passkey_signin", "address", address, "passkey", found.ID)
	return s, nil
}

// failPasskey counts a failed passkey check against the address and says what to answer: a
// *PasskeyFailure with the tries left, or the *SigninLimitError when this was the fifth. The
// log gets the event with a reason word, never anything the browser sent.
func (a *Auth) failPasskey(ctx context.Context, at *attempt, event, address, reason string) error {
	a.event(event, "address", address, "reason", reason)
	triesLeft, err := a.recordFailure(ctx, at)
	var limited *SigninLimitError
	switch {
	case errors.As(err, &limited):
		a.event("signin_limited", "address", address)
		return err
	case err != nil:
		return err
	}
	return &PasskeyFailure{TriesLeft: triesLeft}
}

// recordPasskeyUse saves what a good assertion changed: the counter, the flags, the time.
func (a *Auth) recordPasskeyUse(ctx context.Context, id string, cred *webauthn.Credential) error {
	err := a.store.UpdatePasskeyUse(ctx, id, cred.Authenticator.SignCount, cred.Flags.UserVerified,
		cred.Flags.BackupEligible, cred.Flags.BackupState, a.now().UTC())
	if err != nil {
		return fmt.Errorf("record passkey use: %w", err)
	}
	return nil
}

// admin returns the admin, or ErrNoAdmin.
func (a *Auth) admin(ctx context.Context) (store.AdminRecord, error) {
	admin, err := a.store.GetAdmin(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return store.AdminRecord{}, ErrNoAdmin
	} else if err != nil {
		return store.AdminRecord{}, fmt.Errorf("get admin: %w", err)
	}
	return admin, nil
}

// --- Confirming it's you --------------------------------------------------------------------

// confirmed reports whether the session proved it is the owner within confirmWindow.
func (a *Auth) confirmed(rec store.SessionRecord) bool {
	return rec.VerifiedAt != nil && a.now().UTC().Sub(*rec.VerifiedAt) < confirmWindow
}

// SessionConfirmed reports whether the session (a cookie value) may add a passkey: it signed in,
// or confirmed it's the owner, within the last 10 minutes. A dead session is ErrNotAuthenticated.
func (a *Auth) SessionConfirmed(ctx context.Context, sessionValue string) (bool, error) {
	_, rec, err := a.liveSession(ctx, sessionValue)
	if err != nil {
		return false, err
	}
	return a.confirmed(rec), nil
}

// PasswordSignInAllowed reports whether the password may sign in at consoleDomain: it is on, or it
// is off but no passkey exists for that domain, which keeps a way in after a domain change (R4).
// consoleDomain "" means any passkey counts.
func (a *Auth) PasswordSignInAllowed(ctx context.Context, consoleDomain string) (bool, error) {
	admin, err := a.admin(ctx)
	if err != nil {
		return false, err
	}
	if admin.PasswordSignIn {
		return true, nil
	}
	n, err := a.store.CountPasskeys(ctx, consoleDomain)
	if err != nil {
		return false, fmt.Errorf("check password sign-in: %w", err)
	}
	return n == 0, nil
}

// VerifyPassword confirms a signed-in session is the owner with the password, which counts as a
// sign-in for the next 10 minutes. A wrong password counts against the address like a wrong
// sign-in and is a *SigninFailure (or a *SigninLimitError); when password sign-in is off for
// consoleDomain it is ErrPasswordSignInOff and nothing is checked or counted.
func (a *Auth) VerifyPassword(ctx context.Context, sessionValue, password, consoleDomain, address string) error {
	id, _, err := a.liveSession(ctx, sessionValue)
	if err != nil {
		return err
	}
	admin, err := a.admin(ctx)
	if err != nil {
		return err
	}
	if allowed, err := a.PasswordSignInAllowed(ctx, consoleDomain); err != nil {
		return err
	} else if !allowed {
		return ErrPasswordSignInOff
	}

	at, err := a.beginAttempt(ctx, address)
	if err != nil {
		var limited *SigninLimitError
		if errors.As(err, &limited) {
			a.event("signin_limited", "address", address)
		}
		return err
	}
	defer at.release()
	if !a.check(password, admin.PasswordHash) {
		a.event("password_confirm_failed", "address", address)
		triesLeft, err := a.recordFailure(ctx, at)
		var limited *SigninLimitError
		switch {
		case errors.As(err, &limited):
			a.event("signin_limited", "address", address)
			return err
		case err != nil:
			return err
		}
		return &SigninFailure{TriesLeft: triesLeft}
	}
	if err := a.clearFailures(ctx, at); err != nil {
		return err
	}
	if err := a.store.SetSessionVerified(ctx, id, a.now().UTC()); err != nil {
		return fmt.Errorf("confirm session: %w", err)
	}
	a.event("password_confirmed")
	return nil
}

// BeginPasskeyConfirm starts "Confirm it's you" with a passkey made at rp's domain. Without one
// it is ErrNoPasskey. The ceremony belongs to this session: no other can finish it.
func (a *Auth) BeginPasskeyConfirm(ctx context.Context, sessionValue string, rp RelyingParty, address string) (PasskeyOptions, error) {
	id, _, err := a.liveSession(ctx, sessionValue)
	if err != nil {
		return PasskeyOptions{}, err
	}
	admin, err := a.admin(ctx)
	if err != nil {
		return PasskeyOptions{}, err
	}
	recs, err := a.passkeysAt(ctx, rp.ID)
	if err != nil {
		return PasskeyOptions{}, err
	}
	if len(recs) == 0 {
		return PasskeyOptions{}, &PasskeyDomainError{Err: ErrNoPasskey, Domain: rp.ID}
	}
	w, err := webauthnFor(rp)
	if err != nil {
		return PasskeyOptions{}, err
	}
	assertion, session, err := w.BeginLogin(userWith(admin, recs), webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return PasskeyOptions{}, fmt.Errorf("begin passkey confirm: %w", err)
	}
	return a.keepCeremony(ceremony{kind: ceremonyConfirm, session: *session, rp: rp, address: address,
		sessionHash: id}, assertion.Response)
}

// FinishPasskeyConfirm checks the browser's answer to BeginPasskeyConfirm and confirms the
// session. Failures count against the address, as a failed sign-in does (R11).
func (a *Auth) FinishPasskeyConfirm(ctx context.Context, sessionValue string, rp RelyingParty, ceremonyID string, credential []byte, address string) error {
	id, _, err := a.liveSession(ctx, sessionValue)
	if err != nil {
		return err
	}
	admin, err := a.admin(ctx)
	if err != nil {
		return err
	}
	at, err := a.beginAttempt(ctx, address)
	if err != nil {
		var limited *SigninLimitError
		if errors.As(err, &limited) {
			a.event("signin_limited", "address", address)
		}
		return err
	}
	defer at.release()
	fail := func(reason string) error {
		return a.failPasskey(ctx, at, "passkey_confirm_failed", address, reason)
	}

	c, ok := a.ceremonies.take(a.now().UTC(), ceremonyID)
	if !ok || c.kind != ceremonyConfirm || c.rp != rp || c.sessionHash != id {
		return fail("ceremony")
	}
	if len(credential) > maxCredentialBytes {
		return fail("credential")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		return fail("credential")
	}
	recs, err := a.passkeysAt(ctx, c.rp.ID)
	if err != nil {
		return err
	}
	var found *store.PasskeyRecord
	for i := range recs {
		if bytes.Equal(recs[i].CredentialID, parsed.RawID) {
			found = &recs[i]
		}
	}
	if found == nil {
		return fail("credential")
	}
	w, err := webauthnFor(c.rp)
	if err != nil {
		return err
	}
	cred, err := w.ValidateLogin(userWith(admin, recs), c.session, parsed)
	if err != nil {
		return fail("verification")
	}
	if cred.Authenticator.CloneWarning {
		return fail("counter")
	}

	if err := a.recordPasskeyUse(ctx, found.ID, cred); err != nil {
		return err
	}
	if err := a.clearFailures(ctx, at); err != nil {
		return err
	}
	if err := a.store.SetSessionVerified(ctx, id, a.now().UTC()); err != nil {
		return fmt.Errorf("confirm session: %w", err)
	}
	a.event("passkey_confirmed", "passkey", found.ID)
	return nil
}

// passkeysAt returns the passkeys made at one domain, oldest first.
func (a *Auth) passkeysAt(ctx context.Context, rpID string) ([]store.PasskeyRecord, error) {
	all, err := a.store.ListPasskeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	var out []store.PasskeyRecord
	for _, r := range all {
		if r.RPID == rpID {
			out = append(out, r)
		}
	}
	return out, nil
}

// userWith is the admin with the credentials of recs.
func userWith(admin store.AdminRecord, recs []store.PasskeyRecord) *passkeyUser {
	u := &passkeyUser{handle: admin.WebAuthnHandle, email: admin.Email}
	for _, r := range recs {
		u.credentials = append(u.credentials, credentialOf(r))
	}
	return u
}

// --- Adding a passkey -----------------------------------------------------------------------

// BeginPasskeyRegistration starts adding a passkey at rp's domain. The session must be confirmed
// (ErrNotConfirmed). The admin's WebAuthn user handle is made here the first time. Passkeys
// already stored for the domain are excluded, so one device doesn't register twice.
func (a *Auth) BeginPasskeyRegistration(ctx context.Context, sessionValue string, rp RelyingParty, address string) (PasskeyOptions, error) {
	id, rec, err := a.liveSession(ctx, sessionValue)
	if err != nil {
		return PasskeyOptions{}, err
	}
	if !a.confirmed(rec) {
		return PasskeyOptions{}, ErrNotConfirmed
	}
	admin, err := a.admin(ctx)
	if err != nil {
		return PasskeyOptions{}, err
	}
	if len(admin.WebAuthnHandle) == 0 {
		fresh := make([]byte, passkeyHandleBytes)
		if _, err := rand.Read(fresh); err != nil {
			return PasskeyOptions{}, fmt.Errorf("generate user handle: %w", err)
		}
		// Two first registrations at once agree on whichever handle was stored first.
		if admin.WebAuthnHandle, err = a.store.SetWebAuthnHandle(ctx, fresh); err != nil {
			return PasskeyOptions{}, fmt.Errorf("store user handle: %w", err)
		}
	}
	recs, err := a.passkeysAt(ctx, rp.ID)
	if err != nil {
		return PasskeyOptions{}, err
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(recs))
	for _, r := range recs {
		cred := credentialOf(r)
		exclude = append(exclude, cred.Descriptor())
	}
	w, err := webauthnFor(rp)
	if err != nil {
		return PasskeyOptions{}, err
	}
	creation, session, err := w.BeginRegistration(&passkeyUser{handle: admin.WebAuthnHandle, email: admin.Email},
		webauthn.WithAuthenticatorSelection(passkeySelection()),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithExclusions(exclude))
	if err != nil {
		return PasskeyOptions{}, fmt.Errorf("begin passkey registration: %w", err)
	}
	return a.keepCeremony(ceremony{kind: ceremonyRegister, session: *session, rp: rp, address: address,
		sessionHash: id}, creation.Response)
}

// FinishPasskeyRegistration checks the browser's answer to BeginPasskeyRegistration and stores the
// passkey under name (trimmed; 1 to 64 characters, no control characters: an *app.FieldError on
// "name" otherwise). Anything the authenticator sent that doesn't verify is ErrPasskeyRefused.
// The session must still be confirmed.
func (a *Auth) FinishPasskeyRegistration(ctx context.Context, sessionValue string, rp RelyingParty, name, ceremonyID string, credential []byte) (Passkey, error) {
	name, err := checkPasskeyName(name)
	if err != nil {
		return Passkey{}, err
	}
	id, rec, err := a.liveSession(ctx, sessionValue)
	if err != nil {
		return Passkey{}, err
	}
	if !a.confirmed(rec) {
		return Passkey{}, ErrNotConfirmed
	}
	admin, err := a.admin(ctx)
	if err != nil {
		return Passkey{}, err
	}

	c, ok := a.ceremonies.take(a.now().UTC(), ceremonyID)
	if !ok || c.kind != ceremonyRegister || c.rp != rp || c.sessionHash != id || len(credential) > maxCredentialBytes {
		return Passkey{}, ErrPasskeyRefused
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credential)
	if err != nil {
		return Passkey{}, ErrPasskeyRefused
	}
	w, err := webauthnFor(c.rp)
	if err != nil {
		return Passkey{}, err
	}
	cred, err := w.CreateCredential(&passkeyUser{handle: admin.WebAuthnHandle, email: admin.Email}, c.session, parsed)
	if err != nil || !cred.Flags.UserVerified {
		return Passkey{}, ErrPasskeyRefused
	}

	transports := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		transports[i] = string(t)
	}
	r := store.PasskeyRecord{
		ID: strings.ToLower(rand.Text()), UserHandle: admin.WebAuthnHandle, CredentialID: cred.ID,
		RPID: c.rp.ID, PublicKey: cred.PublicKey, AttestationType: cred.AttestationType,
		AttestationFormat: cred.AttestationFormat, AAGUID: cred.Authenticator.AAGUID,
		SignCount: cred.Authenticator.SignCount, Transports: transports,
		UserPresent: cred.Flags.UserPresent, UserVerified: cred.Flags.UserVerified,
		BackupEligible: cred.Flags.BackupEligible, BackupState: cred.Flags.BackupState,
		Name: name, CreatedAt: a.now().UTC(),
	}
	if err := a.store.InsertPasskey(ctx, r); errors.Is(err, store.ErrAlreadyExists) {
		return Passkey{}, ErrPasskeyRefused // this authenticator credential is stored already
	} else if err != nil {
		return Passkey{}, fmt.Errorf("add passkey: %w", err)
	}
	a.event("passkey_added", "passkey", r.ID)
	return passkeyOf(r), nil
}

// checkPasskeyName trims a name and checks it is 1 to 64 characters without control characters.
func checkPasskeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxPasskeyName {
		return "", app.NewFieldError("name", "must be 1 to %d characters", maxPasskeyName)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", app.NewFieldError("name", "must not contain control characters")
		}
	}
	return name, nil
}

// --- Managing passkeys ----------------------------------------------------------------------

// ListPasskeys returns every passkey, oldest first.
func (a *Auth) ListPasskeys(ctx context.Context) ([]Passkey, error) {
	recs, err := a.store.ListPasskeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	out := make([]Passkey, 0, len(recs))
	for _, r := range recs {
		out = append(out, passkeyOf(r))
	}
	return out, nil
}

// RenamePasskey gives a passkey a new name and returns it. A bad name is an *app.FieldError on
// "name"; an unknown ID is ErrPasskeyNotFound.
func (a *Auth) RenamePasskey(ctx context.Context, id, name string) (Passkey, error) {
	name, err := checkPasskeyName(name)
	if err != nil {
		return Passkey{}, err
	}
	if err := a.store.RenamePasskey(ctx, id, name); errors.Is(err, store.ErrNotFound) {
		return Passkey{}, ErrPasskeyNotFound
	} else if err != nil {
		return Passkey{}, fmt.Errorf("rename passkey: %w", err)
	}
	rec, err := a.store.GetPasskey(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Passkey{}, ErrPasskeyNotFound
	} else if err != nil {
		return Passkey{}, fmt.Errorf("rename passkey: %w", err)
	}
	a.event("passkey_renamed", "passkey", id)
	return passkeyOf(rec), nil
}

// RemovePasskey deletes a passkey. While password sign-in is off, the last passkey of its domain
// can't go (ErrLastPasskey, as a *PasskeyDomainError): that would leave no way in. An unknown ID
// is ErrPasskeyNotFound.
func (a *Auth) RemovePasskey(ctx context.Context, id string) error {
	a.passkeyMu.Lock()
	defer a.passkeyMu.Unlock()
	rec, err := a.store.GetPasskey(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrPasskeyNotFound
	} else if err != nil {
		return fmt.Errorf("remove passkey: %w", err)
	}
	admin, err := a.admin(ctx)
	if err != nil {
		return err
	}
	if !admin.PasswordSignIn {
		n, err := a.store.CountPasskeys(ctx, rec.RPID)
		if err != nil {
			return fmt.Errorf("remove passkey: %w", err)
		}
		if n <= 1 {
			return &PasskeyDomainError{Err: ErrLastPasskey, Domain: rec.RPID}
		}
	}
	return a.deletePasskey(ctx, id, "console")
}

// RemovePasskeyUnchecked deletes a passkey without the last-passkey rule. It is for `caravana
// admin passkeys remove`: root on the host is the owner, and reset-password is the way back in.
func (a *Auth) RemovePasskeyUnchecked(ctx context.Context, id string) error {
	a.passkeyMu.Lock()
	defer a.passkeyMu.Unlock()
	return a.deletePasskey(ctx, id, "host")
}

func (a *Auth) deletePasskey(ctx context.Context, id, how string) error {
	if err := a.store.DeletePasskey(ctx, id); errors.Is(err, store.ErrNotFound) {
		return ErrPasskeyNotFound
	} else if err != nil {
		return fmt.Errorf("remove passkey: %w", err)
	}
	a.event("passkey_removed", "passkey", id, "how", how)
	return nil
}

// SignInSettings says whether the password may sign in and how many passkeys there are.
func (a *Auth) SignInSettings(ctx context.Context) (SignInSettings, error) {
	admin, err := a.admin(ctx)
	if err != nil {
		return SignInSettings{}, err
	}
	n, err := a.store.CountPasskeys(ctx, "")
	if err != nil {
		return SignInSettings{}, fmt.Errorf("count passkeys: %w", err)
	}
	return SignInSettings{PasswordSignIn: admin.PasswordSignIn, Passkeys: n}, nil
}

// SetPasswordSignIn turns password sign-in on or off. Turning it off needs a passkey made at
// consoleDomain ("" means any passkey): otherwise it is ErrNoPasskey, as a *PasskeyDomainError.
func (a *Auth) SetPasswordSignIn(ctx context.Context, on bool, consoleDomain string) (SignInSettings, error) {
	a.passkeyMu.Lock()
	defer a.passkeyMu.Unlock()
	admin, err := a.admin(ctx)
	if err != nil {
		return SignInSettings{}, err
	}
	if !on {
		n, err := a.store.CountPasskeys(ctx, consoleDomain)
		if err != nil {
			return SignInSettings{}, fmt.Errorf("set password sign-in: %w", err)
		}
		if n == 0 {
			return SignInSettings{}, &PasskeyDomainError{Err: ErrNoPasskey, Domain: consoleDomain}
		}
	}
	if admin.PasswordSignIn != on {
		if err := a.store.SetPasswordSignIn(ctx, on); err != nil {
			return SignInSettings{}, fmt.Errorf("set password sign-in: %w", err)
		}
		if on {
			a.event("password_signin_on")
		} else {
			a.event("password_signin_off")
		}
	}
	return a.SignInSettings(ctx)
}

// passwordSignInOn turns password sign-in on (ResetPassword's step), logging only a change.
func (a *Auth) passwordSignInOn(ctx context.Context) error {
	a.passkeyMu.Lock()
	defer a.passkeyMu.Unlock()
	admin, err := a.admin(ctx)
	if err != nil {
		return err
	}
	if admin.PasswordSignIn {
		return nil
	}
	if err := a.store.SetPasswordSignIn(ctx, true); err != nil {
		return fmt.Errorf("turn password sign-in on: %w", err)
	}
	a.event("password_signin_on")
	return nil
}
