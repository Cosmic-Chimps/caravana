package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PersonRecord is a person as stored (feature 053): the password only as a hash,
// and an invite only as the hash of its token.
type PersonRecord struct {
	ID    string
	Email string // trimmed, lower case
	// OrgRole is owner, admin or member; State is invited or active.
	OrgRole string
	State   string
	// PasswordHash is pbkdf2-sha256$<iterations>$<salt>$<key>, "" while invited.
	PasswordHash      string
	PasswordChangedAt *time.Time
	// PasswordSignIn is false when only passkeys may sign in (feature 033).
	PasswordSignIn bool
	// WebAuthnHandle is the person's WebAuthn user ID: 32 random bytes made at the
	// first passkey registration, nil before that.
	WebAuthnHandle []byte
	// InviteHash is the hex SHA-256 of the invite token, "" once it was accepted.
	InviteHash      string
	InviteExpiresAt *time.Time
	InvitedBy       string // the inviter's email, kept as text
	EndsAt          *time.Time
	CreatedAt       time.Time
}

// ProjectRoleRecord is a person's role in one project, with the overrides for some of its
// environments (role none, view, deploy or maintain).
type ProjectRoleRecord struct {
	Project      string
	Role         string // view, deploy or maintain
	EndsAt       *time.Time
	Environments map[string]string
}

const personColumns = "id, email, org_role, state, password_hash, password_changed_at, password_signin, " +
	"webauthn_handle, invite_hash, invite_expires_at, invited_by, ends_at, created_at"

func scanPerson(sc scanner) (PersonRecord, error) {
	var (
		p                                         PersonRecord
		changed, inviteHash, inviteExp, invitedBy sql.NullString
		ends                                      sql.NullString
		created                                   string
	)
	if err := sc.Scan(&p.ID, &p.Email, &p.OrgRole, &p.State, &p.PasswordHash, &changed, &p.PasswordSignIn,
		&p.WebAuthnHandle, &inviteHash, &inviteExp, &invitedBy, &ends, &created); err != nil {
		return PersonRecord{}, err
	}
	p.InviteHash, p.InvitedBy = inviteHash.String, invitedBy.String
	var err error
	if p.PasswordChangedAt, err = parseNullTime(changed); err != nil {
		return PersonRecord{}, err
	}
	if p.InviteExpiresAt, err = parseNullTime(inviteExp); err != nil {
		return PersonRecord{}, err
	}
	if p.EndsAt, err = parseNullTime(ends); err != nil {
		return PersonRecord{}, err
	}
	if p.CreatedAt, err = parseTime(created); err != nil {
		return PersonRecord{}, err
	}
	return p, nil
}

// CreatePerson stores p and its roles in one transaction. An email or invite hash that is
// already stored is ErrAlreadyExists; a role naming a project or environment that doesn't
// exist is an error naming it (ErrNotFound).
func (s *Store) CreatePerson(ctx context.Context, p PersonRecord, roles []ProjectRoleRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create person: %w", err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	_, err = tx.ExecContext(ctx, "INSERT INTO people ("+personColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		p.ID, p.Email, p.OrgRole, p.State, p.PasswordHash, nullTime(p.PasswordChangedAt), p.PasswordSignIn,
		p.WebAuthnHandle, nullString(p.InviteHash), nullTime(p.InviteExpiresAt), nullString(p.InvitedBy),
		nullTime(p.EndsAt), formatTime(p.CreatedAt))
	if isUnique(err) {
		return fmt.Errorf("person %q: %w", p.Email, ErrAlreadyExists)
	} else if err != nil {
		return fmt.Errorf("create person %q: %w", p.Email, err)
	}
	if err := insertRoles(ctx, tx, p.ID, roles); err != nil {
		return fmt.Errorf("create person %q: %w", p.Email, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create person %q: %w", p.Email, err)
	}
	return nil
}

// insertRoles writes the project and environment rows of one person.
func insertRoles(ctx context.Context, tx *sql.Tx, personID string, roles []ProjectRoleRecord) error {
	for _, r := range roles {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO project_roles (person_id, project, role, ends_at) VALUES (?, ?, ?, ?)",
			personID, r.Project, r.Role, nullTime(r.EndsAt)); isForeignKey(err) {
			return fmt.Errorf("project %q: %w", r.Project, ErrNotFound)
		} else if isUnique(err) {
			return fmt.Errorf("project %q given twice: %w", r.Project, ErrAlreadyExists)
		} else if err != nil {
			return fmt.Errorf("role in project %q: %w", r.Project, err)
		}
		for env, role := range r.Environments {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO environment_roles (person_id, project, environment, role) VALUES (?, ?, ?, ?)",
				personID, r.Project, env, role); isForeignKey(err) {
				return fmt.Errorf("environment %q of project %q: %w", env, r.Project, ErrNotFound)
			} else if err != nil {
				return fmt.Errorf("role in %s/%s: %w", r.Project, env, err)
			}
		}
	}
	return nil
}

// GetPerson returns the person with that ID, or ErrNotFound.
func (s *Store) GetPerson(ctx context.Context, id string) (PersonRecord, error) {
	return s.onePerson(ctx, "id", id)
}

// GetPersonByEmail returns the person with that (already lower-cased) email, or ErrNotFound.
func (s *Store) GetPersonByEmail(ctx context.Context, email string) (PersonRecord, error) {
	return s.onePerson(ctx, "email", email)
}

// GetPersonByInvite returns the person whose invite token has that hex SHA-256, or ErrNotFound.
func (s *Store) GetPersonByInvite(ctx context.Context, hash string) (PersonRecord, error) {
	return s.onePerson(ctx, "invite_hash", hash)
}

// onePerson reads the person whose column (one of ours, never user input) equals value.
func (s *Store) onePerson(ctx context.Context, column, value string) (PersonRecord, error) {
	p, err := scanPerson(s.db.QueryRowContext(ctx, "SELECT "+personColumns+" FROM people WHERE "+column+" = ?", value))
	if errors.Is(err, sql.ErrNoRows) {
		return PersonRecord{}, fmt.Errorf("person: %w", ErrNotFound)
	} else if err != nil {
		return PersonRecord{}, fmt.Errorf("get person: %w", err)
	}
	return p, nil
}

// ListPeople returns everyone, the oldest first.
func (s *Store) ListPeople(ctx context.Context) ([]PersonRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+personColumns+" FROM people ORDER BY created_at, rowid")
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	defer rows.Close()
	var out []PersonRecord
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, fmt.Errorf("list people: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	return out, nil
}

// FirstOwner returns the oldest owner, or ErrNotFound while nobody exists (an owner always
// exists once anyone does).
func (s *Store) FirstOwner(ctx context.Context) (PersonRecord, error) {
	p, err := scanPerson(s.db.QueryRowContext(ctx,
		"SELECT "+personColumns+" FROM people WHERE org_role = 'owner' ORDER BY created_at, rowid LIMIT 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return PersonRecord{}, fmt.Errorf("owner: %w", ErrNotFound)
	} else if err != nil {
		return PersonRecord{}, fmt.Errorf("get first owner: %w", err)
	}
	return p, nil
}

// CountOwners counts the owners.
func (s *Store) CountOwners(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM people WHERE org_role = 'owner'").Scan(&n); err != nil {
		return 0, fmt.Errorf("count owners: %w", err)
	}
	return n, nil
}

// UpdatePerson sets a person's organization role and end date. An unknown ID is ErrNotFound.
func (s *Store) UpdatePerson(ctx context.Context, id, orgRole string, endsAt *time.Time) error {
	res, err := s.db.ExecContext(ctx, "UPDATE people SET org_role = ?, ends_at = ? WHERE id = ?",
		orgRole, nullTime(endsAt), id)
	if err != nil {
		return fmt.Errorf("update person: %w", err)
	}
	return personChanged(res, "update person", id)
}

// SetPersonRoles replaces every project and environment row of a person in one transaction.
// An unknown person is ErrNotFound, and so is a project or environment that doesn't exist; then
// nothing changes.
func (s *Store) SetPersonRoles(ctx context.Context, id string, roles []ProjectRoleRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set person roles: %w", err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	var one int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM people WHERE id = ?", id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("set person roles: person %q: %w", id, ErrNotFound)
	} else if err != nil {
		return fmt.Errorf("set person roles: %w", err)
	}
	// The project rows' cascade takes the environment rows with them.
	if _, err := tx.ExecContext(ctx, "DELETE FROM project_roles WHERE person_id = ?", id); err != nil {
		return fmt.Errorf("set person roles: %w", err)
	}
	if err := insertRoles(ctx, tx, id, roles); err != nil {
		return fmt.Errorf("set person roles: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set person roles: %w", err)
	}
	return nil
}

// PersonRoles returns a person's project roles with their environment overrides, by project.
func (s *Store) PersonRoles(ctx context.Context, id string) ([]ProjectRoleRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT project, role, ends_at FROM project_roles WHERE person_id = ? ORDER BY project", id)
	if err != nil {
		return nil, fmt.Errorf("person roles: %w", err)
	}
	var out []ProjectRoleRecord
	for rows.Next() {
		var r ProjectRoleRecord
		var ends sql.NullString
		if err := rows.Scan(&r.Project, &r.Role, &ends); err != nil {
			rows.Close()
			return nil, fmt.Errorf("person roles: %w", err)
		}
		if r.EndsAt, err = parseNullTime(ends); err != nil {
			rows.Close()
			return nil, fmt.Errorf("person roles: %w", err)
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close() // the single connection must be free for the next query
	if err != nil {
		return nil, fmt.Errorf("person roles: %w", err)
	}

	envs, err := s.db.QueryContext(ctx,
		"SELECT project, environment, role FROM environment_roles WHERE person_id = ? ORDER BY project, environment", id)
	if err != nil {
		return nil, fmt.Errorf("person roles: %w", err)
	}
	defer envs.Close()
	for envs.Next() {
		var project, env, role string
		if err := envs.Scan(&project, &env, &role); err != nil {
			return nil, fmt.Errorf("person roles: %w", err)
		}
		for i := range out {
			if out[i].Project == project {
				if out[i].Environments == nil {
					out[i].Environments = map[string]string{}
				}
				out[i].Environments[env] = role
			}
		}
	}
	if err := envs.Err(); err != nil {
		return nil, fmt.Errorf("person roles: %w", err)
	}
	return out, nil
}

// SetInvite gives a person a new invite token (its hex SHA-256) and expiry, which voids the old
// one. An unknown ID is ErrNotFound.
func (s *Store) SetInvite(ctx context.Context, id, hash string, expires time.Time) error {
	res, err := s.db.ExecContext(ctx, "UPDATE people SET invite_hash = ?, invite_expires_at = ? WHERE id = ?",
		hash, formatTime(expires), id)
	if isUnique(err) {
		return fmt.Errorf("invite token: %w", ErrAlreadyExists)
	} else if err != nil {
		return fmt.Errorf("set invite: %w", err)
	}
	return personChanged(res, "set invite", id)
}

// AcceptInvite makes an invited person active with the given password hash and clears the
// invite, in one statement that only matches while the invite is still that token: two accepts
// of one token can't both win. A person that isn't invited or whose token differs is ErrNotFound.
func (s *Store) AcceptInvite(ctx context.Context, id, inviteHash, passwordHash string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE people SET state = 'active', password_hash = ?, password_changed_at = ?, invite_hash = NULL, invite_expires_at = NULL
		WHERE id = ? AND state = 'invited' AND invite_hash = ?`,
		passwordHash, formatTime(at), id, inviteHash)
	if err != nil {
		return fmt.Errorf("accept invite: %w", err)
	}
	return personChanged(res, "accept invite", id)
}

// DeletePerson removes a person, and their sessions, API tokens, passkeys and roles, in one
// transaction: their next call is refused. An unknown ID is ErrNotFound.
func (s *Store) DeletePerson(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete person: %w", err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	// The foreign keys would do the same, but the credentials are the point: say so.
	for _, table := range []string{"sessions", "api_tokens", "passkeys", "environment_roles", "project_roles"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE person_id = ?", id); err != nil {
			return fmt.Errorf("delete person: %s: %w", table, err)
		}
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM people WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete person: %w", err)
	}
	if err := personChanged(res, "delete person", id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete person: %w", err)
	}
	return nil
}

// SetPassword replaces a person's password hash and deletes every session of theirs but
// keepSession (an ID hash; "" deletes them all), in one transaction: a new password never
// leaves the old sessions alive. An unknown person is ErrNotFound and nothing changes.
func (s *Store) SetPassword(ctx context.Context, personID, hash string, at time.Time, keepSession string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	res, err := tx.ExecContext(ctx,
		"UPDATE people SET password_hash = ?, password_changed_at = ? WHERE id = ?",
		hash, formatTime(at), personID)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if err := personChanged(res, "set password", personID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE person_id = ? AND id_hash != ?", personID, keepSession); err != nil {
		return fmt.Errorf("set password: delete sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return nil
}

// SetPasswordSignIn turns a person's password sign-in on or off. An unknown person is ErrNotFound.
func (s *Store) SetPasswordSignIn(ctx context.Context, personID string, on bool) error {
	res, err := s.db.ExecContext(ctx, "UPDATE people SET password_signin = ? WHERE id = ?", on, personID)
	if err != nil {
		return fmt.Errorf("set password sign-in: %w", err)
	}
	return personChanged(res, "set password sign-in", personID)
}

// SetWebAuthnHandle stores a person's WebAuthn user handle, but only when none is stored yet,
// in one statement: two first registrations at once agree on the one that won. It returns the
// stored handle (the one given, or the earlier one). An unknown person is ErrNotFound.
func (s *Store) SetWebAuthnHandle(ctx context.Context, personID string, handle []byte) ([]byte, error) {
	var stored []byte
	err := s.db.QueryRowContext(ctx,
		"UPDATE people SET webauthn_handle = COALESCE(webauthn_handle, ?) WHERE id = ? RETURNING webauthn_handle",
		handle, personID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("set webauthn handle: person %q: %w", personID, ErrNotFound)
	} else if err != nil {
		return nil, fmt.Errorf("set webauthn handle: %w", err)
	}
	return stored, nil
}

// DeleteSessionsOf deletes every session of a person (an ended person's).
func (s *Store) DeleteSessionsOf(ctx context.Context, personID string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE person_id = ?", personID); err != nil {
		return fmt.Errorf("delete sessions of person: %w", err)
	}
	return nil
}

// personChanged turns "no row changed" into ErrNotFound.
func personChanged(res sql.Result, what, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: person %q: %w", what, id, ErrNotFound)
	}
	return nil
}
