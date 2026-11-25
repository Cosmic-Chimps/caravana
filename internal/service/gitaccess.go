package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Git access (feature 050, plan D3): how a built service's build reads its
// repository. The private half of a deploy key and a token live only in a Nomad
// Variable (deploy.GitAccessPath); this file makes them, replaces and removes
// them, and answers with what is safe to show. Nothing here logs or returns a
// secret, and no error holds one.

// ErrGitAccessNotFound: the service has no deploy key or token to remove.
var ErrGitAccessNotFound = errors.New("the service has no Git access")

// The kinds a GitAccessView says. "none" is a service with no stored access.
const (
	GitAccessNone = "none"
)

// GitAccessView is a service's Git access as it is shown: the kind, and for a
// deploy key the public half and its fingerprint; for a token its user name.
// The private key and the token are never in it.
type GitAccessView struct {
	// Kind is "none", app.BuildAccessDeployKey or app.BuildAccessToken.
	Kind string
	// PublicKey is the authorized_keys line of a deploy key, with the comment
	// "caravana <app>/<service>"; Fingerprint is its SHA256 fingerprint.
	PublicKey, Fingerprint string
	// Username is a token's user name.
	Username string
	// CreatedAt is when the access was made; zero for none.
	CreatedAt time.Time
}

// maxGitToken and maxGitUsername are the limits of what a token's form takes.
const (
	maxGitToken    = 4096
	maxGitUsername = 255
)

// defaultGitUsername is the user name a token is sent with when the owner gives
// none: the one GitHub's tokens take, and what GitLab and Gitea accept.
const defaultGitUsername = "x-access-token"

// gitAccessService finds the service the access is for. An unknown app or
// service is store.ErrNotFound; a service that isn't built is a field error
// when need is true (making access for it is pointless), else it is allowed
// (reading it answers none).
func (s *Service) gitAccessService(ctx context.Context, appID, service string, needBuilt bool) error {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	for _, svc := range rec.App.Services {
		if svc.Name != service {
			continue
		}
		if needBuilt && svc.Build == nil {
			return app.NewFieldError("service", "service %q is not built from a Git repository, so it has no Git access", service)
		}
		return nil
	}
	return fmt.Errorf("app %q has no service %q: %w", appID, service, store.ErrNotFound)
}

// GitAccess returns the service's Git access without its secrets.
func (s *Service) GitAccess(ctx context.Context, appID, service string) (GitAccessView, error) {
	if err := s.gitAccessService(ctx, appID, service, false); err != nil {
		return GitAccessView{}, err
	}
	g, found, err := s.deployer.ReadGitAccess(ctx, appID, service)
	if err != nil {
		return GitAccessView{}, gitStoreError("read", err)
	}
	if !found {
		return GitAccessView{Kind: GitAccessNone}, nil
	}
	return viewOfGitAccess(g), nil
}

func viewOfGitAccess(g deploy.GitAccess) GitAccessView {
	v := GitAccessView{Kind: g.Kind, CreatedAt: g.CreatedAt}
	switch g.Kind {
	case app.BuildAccessDeployKey:
		v.PublicKey = g.PublicKey
		if key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(g.PublicKey)); err == nil {
			v.Fingerprint = ssh.FingerprintSHA256(key)
		}
	case app.BuildAccessToken:
		v.Username = g.Username
	default:
		return GitAccessView{Kind: GitAccessNone}
	}
	return v
}

// NewDeployKey makes an ed25519 key pair for the service, keeps the private key
// in a Nomad Variable and answers with the public key to add to the
// repository's deploy keys. It replaces any key or token the service had.
func (s *Service) NewDeployKey(ctx context.Context, appID, service string) (GitAccessView, error) {
	if err := s.gitAccessService(ctx, appID, service, true); err != nil {
		return GitAccessView{}, err
	}
	private, public, err := newDeployKey(rand.Reader, appID, service)
	if err != nil {
		return GitAccessView{}, err
	}
	g := deploy.GitAccess{Kind: app.BuildAccessDeployKey, PrivateKey: private, PublicKey: public, CreatedAt: time.Now().UTC()}
	was := s.gitAccessBefore(ctx, appID, service)
	if err := s.deployer.SaveGitAccess(ctx, appID, service, g); err != nil {
		return GitAccessView{}, gitStoreError("write", err)
	}
	s.appChanged(appID)
	s.noteGitAccess(ctx, audit.ActionCreated, appID, service, was, &g)
	return viewOfGitAccess(g), nil
}

// newDeployKey makes the key pair: the private key in OpenSSH's PEM form, the
// public key as an authorized_keys line with the comment "caravana
// <app>/<service>". random is crypto/rand's reader in production.
func newDeployKey(random interface{ Read([]byte) (int, error) }, appID, service string) (private, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(random)
	if err != nil {
		return "", "", fmt.Errorf("make a deploy key: %w", err)
	}
	comment := "caravana " + appID + "/" + service
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", fmt.Errorf("make a deploy key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("make a deploy key: %w", err)
	}
	private = string(pem.EncodeToMemory(block))
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment
	return private, line, nil
}

// SetGitToken keeps a token for the service, replacing any key or token it had.
// The user name is optional (x-access-token). The token is 1 to 4096 printable
// characters; it is never shown again.
func (s *Service) SetGitToken(ctx context.Context, appID, service, username, token string) (GitAccessView, error) {
	if err := s.gitAccessService(ctx, appID, service, true); err != nil {
		return GitAccessView{}, err
	}
	username, token = strings.TrimSpace(username), strings.TrimSpace(token)
	if username == "" {
		username = defaultGitUsername
	}
	if err := checkGitToken(username, token); err != nil {
		return GitAccessView{}, err
	}
	g := deploy.GitAccess{Kind: app.BuildAccessToken, Username: username, Token: token, CreatedAt: time.Now().UTC()}
	was := s.gitAccessBefore(ctx, appID, service)
	if err := s.deployer.SaveGitAccess(ctx, appID, service, g); err != nil {
		return GitAccessView{}, gitStoreError("write", err)
	}
	s.appChanged(appID)
	s.noteGitAccess(ctx, audit.ActionCreated, appID, service, was, &g)
	return viewOfGitAccess(g), nil
}

// checkGitToken checks what a token's form gave. The errors never echo the token.
func checkGitToken(username, token string) error {
	switch {
	case username != "" && (len(username) > maxGitUsername || !printableNoSpace(username)):
		return app.NewFieldError("username", "must be 1 to %d printable characters without spaces", maxGitUsername)
	case token == "":
		return app.NewFieldError("token", "is required")
	case len(token) > maxGitToken || !printableNoSpace(token):
		return app.NewFieldError("token", "must be at most %d printable characters without spaces", maxGitToken)
	}
	return nil
}

func printableNoSpace(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// RemoveGitAccess forgets the service's deploy key or token. A service that has
// none is ErrGitAccessNotFound.
func (s *Service) RemoveGitAccess(ctx context.Context, appID, service string) error {
	if err := s.gitAccessService(ctx, appID, service, false); err != nil {
		return err
	}
	was, found, err := s.deployer.ReadGitAccess(ctx, appID, service)
	if err != nil {
		return gitStoreError("read", err)
	}
	if !found {
		return fmt.Errorf("service %q of app %q: %w", service, appID, ErrGitAccessNotFound)
	}
	if err := s.deployer.DeleteGitAccess(ctx, appID, service); err != nil {
		return gitStoreError("delete", err)
	}
	s.appChanged(appID)
	s.noteGitAccess(ctx, audit.ActionDeleted, appID, service, &was, nil)
	return nil
}

// gitAccessBefore is the service's Git access as it is now, nil when it has none or can't be read: the
// log compares it with the new one by kind and user name, never by key or token.
func (s *Service) gitAccessBefore(ctx context.Context, appID, service string) *deploy.GitAccess {
	g, found, err := s.deployer.ReadGitAccess(ctx, appID, service)
	if err != nil || !found {
		return nil
	}
	return &g
}

// noteGitAccess notes a change of a service's Git access. The views hold the kind and the user name, and
// the key or token only as a redacted field (Diff shows "set", never the value).
func (s *Service) noteGitAccess(ctx context.Context, action audit.Action, appID, service string, before, after *deploy.GitAccess) {
	view := func(g *deploy.GitAccess) any {
		if g == nil {
			return nil
		}
		m := map[string]any{"kind": string(g.Kind)}
		if g.Username != "" {
			m["username"] = g.Username
		}
		switch g.Kind {
		case app.BuildAccessToken:
			m["token"] = "stored"
		case app.BuildAccessDeployKey:
			m["privateKey"] = "stored"
			m["publicKey"] = g.PublicKey // public: the owner copies it into the repository's settings
		}
		return m
	}
	r := appResource(appID)
	r.Kind, r.ID, r.Name = audit.KindGitAccess, appID+"/"+service, service
	if before != nil && after != nil {
		action = audit.ActionUpdated
	}
	b, a := view(before), view(after)
	// A new key or token replaces the old one even when the view says the same: say so.
	changes := audit.Diff(b, a)
	if before != nil && after != nil && len(changes) == 0 {
		changes = []audit.Change{{Path: "secret", Op: audit.OpChanged}}
	}
	s.audit(ctx, audit.Entry{Action: action, Resource: r, Changes: changes})
}

// gitStoreError words a failure of Nomad's variables as an unavailable secret
// store, which the API answers 502 (the text is Nomad's own, never a value).
func gitStoreError(action string, err error) error {
	return fmt.Errorf("%s the Git access: %w (%v)", action, secrets.ErrUnavailable, err)
}
