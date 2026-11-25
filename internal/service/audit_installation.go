package service

import (
	"context"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Notes for the installation's own settings and accounts (feature 057, phase C): how each is named in an
// entry, and the one way a secret takes part in a diff.
//
// A setting that holds a secret (a registry, a destination, a channel, a provider, a DNS zone) is diffed
// as a map of what SQLite stores, which holds no secret, plus one leaf under a key audit.Redacted hides
// ("secret", "password", "token", ...). With secretIn that leaf reads "stored" on both sides when the
// setting has a secret, and "stored+" on the after side when the request carried a new one, so Diff shows
// `set` for a first secret, `changed` for a replaced one and nothing for a kept one, and never a value.

// settingsResource is a settings resource: the ID says which (updates, templates, builds, cleanup, scans,
// previews, audit, sign-in).
func settingsResource(id string) audit.Resource {
	return audit.Resource{Kind: audit.KindSettings, ID: id, Name: id}
}

func namedResource(kind audit.Kind, id, name string) audit.Resource {
	if name == "" {
		name = id
	}
	return audit.Resource{Kind: kind, ID: id, Name: name}
}

// secretIn puts the secret leaf of a setting into view under key. stored says the setting has a secret
// already, sent that this request carried one (a before view passes sent false).
func secretIn(view map[string]any, key string, stored, sent bool) {
	switch {
	case sent:
		view[key] = storedSecretMarker + "+"
	case stored:
		view[key] = storedSecretMarker
	}
}

// noteSetting notes a change of a setting resource.
func (s *Service) noteSetting(ctx context.Context, action audit.Action, res audit.Resource, rec *audit.Record, changes ...audit.Change) {
	s.audit(ctx, audit.Entry{Action: action, Resource: res, Record: rec, Changes: changes})
}

// diffMaps is audit.Diff of two views; a nil before is a creation.
func diffMaps(before, after map[string]any) []audit.Change {
	if before == nil {
		return audit.Diff(nil, after)
	}
	return audit.Diff(before, after)
}

// channelResource names a notification channel: its ID and its name.
func channelResource(row store.NotificationChannel) audit.Resource {
	return namedResource(audit.KindNotificationChannel, row.ID, row.Name)
}

// channelSecrets says which of a channel's secrets are set (stored) or came with a request (sent): the
// address of a Slack, Discord or ntfy channel, the token of an ntfy or Telegram channel, the password of an
// email channel, the signing secret of a webhook. None is ever shown.
type channelSecrets struct{ url, token, password, signing bool }

func channelSecretsOf(items map[string]string) channelSecrets {
	return channelSecrets{url: items[secretURL] != "", token: items[secretToken] != "",
		password: items[secretPassword] != "", signing: items[secretSigningSecret] != ""}
}

// with is c plus what sent adds.
func (c channelSecrets) with(sent channelSecrets) channelSecrets {
	return channelSecrets{url: c.url || sent.url, token: c.token || sent.token,
		password: c.password || sent.password, signing: c.signing || sent.signing}
}

// channelAuditView is what a diff of a channel reads: the row (its address as the console shows it, masked
// where the address is the secret) and one redacted leaf per secret.
func channelAuditView(row store.NotificationChannel, stored, sent channelSecrets) map[string]any {
	address := row.Address
	if row.Kind != notify.KindWebhook && wholeURL(address) {
		address = shownAddress(row.Kind, address)
	}
	m := map[string]any{"kind": row.Kind, "name": row.Name, "address": address, "events": row.Events, "paused": row.Paused}
	settings := decodeSettings(row.Settings)
	if settings.Email != nil {
		m["email"] = settings.Email
	}
	if settings.Telegram != nil {
		m["telegram"] = map[string]any{"chatId": settings.Telegram.ChatID, "apiUrl": settings.Telegram.APIURL}
	}
	secretIn(m, "secret", stored.url || stored.signing, sent.url || sent.signing)
	secretIn(m, "token", stored.token, sent.token)
	secretIn(m, "password", stored.password, sent.password)
	return m
}

// serverResource names a server: its ID and its name (empty while a join waits for its machine).
func serverResource(id, name string) audit.Resource {
	if id == "" {
		id = name
	}
	if name == "" {
		name = "(waiting for its machine)"
	}
	return audit.Resource{Kind: audit.KindServer, ID: id, Name: name}
}

// noteUpdateSettings notes a change of the update settings (the release check, automatic updates); the
// same settings changed twice in one request are one entry. A change that moves nothing is none.
func (s *Service) noteUpdateSettings(ctx context.Context, before, after map[string]any) {
	changes := audit.Diff(before, after)
	if len(changes) == 0 {
		s.noChange(ctx)
		return
	}
	s.noteSetting(ctx, audit.ActionUpdated, settingsResource("updates"), nil, changes...)
}

func buildSettingsView(b BuildSettings) map[string]any {
	return map[string]any{"memoryMB": b.MemoryMB, "cpuMHz": b.CPUMHz, "timeoutMinutes": b.TimeoutMinutes,
		"concurrency": b.Concurrency, "cacheMB": b.CacheMB}
}

func scanSettingsView(v ScanSettings) map[string]any {
	return map[string]any{"notifyLevel": v.NotifyLevel, "refuseFixableCritical": v.RefuseFixableCritical,
		"dailyHour": v.DailyHour, "databaseUrl": v.DatabaseURL}
}

// personResource names a person: their ID and their email.
func personResource(id, email string) audit.Resource {
	return namedResource(audit.KindPerson, id, email)
}

// accountResource names a person's own account (their password, their sign-ins).
func accountResource(id, email string) audit.Resource {
	return namedResource(audit.KindAccount, id, email)
}

// personAuditView is what a diff of a person reads: the role, the end date and, for a member, each
// project's role with the environments that override it.
func personAuditView(p Person) map[string]any {
	m := map[string]any{"orgRole": string(p.OrgRole)}
	if p.EndsAt != nil {
		m["endsAt"] = p.EndsAt.UTC().Format(time.RFC3339)
	}
	var projects []any
	for _, pr := range p.Projects {
		item := map[string]any{"name": pr.Project, "role": string(pr.Role)}
		if pr.EndsAt != nil {
			item["endsAt"] = pr.EndsAt.UTC().Format(time.RFC3339)
		}
		if len(pr.Environments) > 0 {
			envs := map[string]any{}
			for env, role := range pr.Environments {
				envs[env] = string(role)
			}
			item["environments"] = envs
		}
		projects = append(projects, item)
	}
	if projects != nil {
		m["projects"] = projects
	}
	return m
}

// passkeyResource names a passkey by our own ID and the name its person gave it: never its credential ID.
func passkeyResource(id, name string) audit.Resource {
	return namedResource(audit.KindPasskey, id, name)
}

// actorUnlessKnown is the person as the actor of a note, unless the context already says who acts (a
// principal: the person, or the token they came with) or the host's CLI runs it (door host), where the
// recorder's own rules decide.
func (a *Auth) actorUnlessKnown(ctx context.Context, personID, email string) audit.Actor {
	if _, ok := PrincipalFrom(ctx); ok || doorOf(ctx) == audit.DoorHost || AuditScopeFrom(ctx) != nil && AuditScopeFrom(ctx).hasActor() {
		return audit.Actor{}
	}
	return audit.Actor{Kind: audit.ActorPerson, Email: email, PersonID: personID}
}

// actorFor is actorUnlessKnown for a person known only by ID.
func (a *Auth) actorFor(ctx context.Context, personID string) audit.Actor {
	if _, ok := PrincipalFrom(ctx); ok || doorOf(ctx) == audit.DoorHost || AuditScopeFrom(ctx) != nil && AuditScopeFrom(ctx).hasActor() {
		return audit.Actor{}
	}
	p, err := a.store.GetPerson(ctx, personID)
	if err != nil {
		return audit.Actor{}
	}
	return audit.Actor{Kind: audit.ActorPerson, Email: p.Email, PersonID: p.ID}
}

// noteSignedIn notes a sign-in: the person is the actor, with the address it came from.
func (a *Auth) noteSignedIn(ctx context.Context, p store.PersonRecord, address, method string) {
	a.audit(ctx, audit.Entry{Action: audit.ActionSignedIn, Resource: accountResource(p.ID, p.Email), Address: address,
		Actor:   audit.Actor{Kind: audit.ActorPerson, Email: p.Email, PersonID: p.ID},
		Changes: []audit.Change{{Path: "method", Op: audit.OpAdded, To: method}}})
}

// noteSignInFailed notes a failed sign-in: nobody acts, and the typed email is kept only when it reads as
// one plain address (AnonymousActor), so a password typed into the email box is never stored.
func (a *Auth) noteSignInFailed(ctx context.Context, typed, address string) {
	a.audit(ctx, audit.Entry{Action: audit.ActionSignInFailed, Resource: audit.Resource{Kind: audit.KindAccount},
		Address: address, Actor: AnonymousActor(strings.TrimSpace(typed))})
}
