package store

import (
	"context"
	"fmt"
	"time"
)

// AppTemplate is the template an app was installed from (feature 035): its
// name and version at that time. Both are empty for an app that wasn't.
type AppTemplate struct {
	Name    string
	Version string
}

// Set says the app came from a template.
func (t AppTemplate) Set() bool { return t.Name != "" }

// SetAppTemplate records the template an app was installed from. An unknown app
// is ErrNotFound. It touches no other column, so the app's revision and update
// time stay as they were.
func (s *Store) SetAppTemplate(ctx context.Context, appID, name, version string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE apps SET template_name = ?, template_version = ? WHERE id = ?", name, version, appID)
	if err != nil {
		return fmt.Errorf("set template of app %q: %w", appID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("set template of app %q: %w", appID, err)
	} else if n == 0 {
		return fmt.Errorf("app %q: %w", appID, ErrNotFound)
	}
	return nil
}

// TemplateCatalogRow is the one row about the template catalog: the owner's
// setting and the newest verified catalog fetched from the catalog repository.
// A field the check hasn't filled is empty (a zero time, an empty string, no
// bytes).
type TemplateCatalogRow struct {
	// Checking is the owner's setting; false means no request is made at all.
	// A new store has it on.
	Checking bool
	// Bundle is the kept catalog.json, verified against its sum; nil until a
	// newer one than the built-in is kept.
	Bundle []byte
	// BundleSum is the SHA-256 (hex) of Bundle, or of the newest bundle seen
	// when it wasn't kept.
	BundleSum string
	// Version is the kept bundle's catalog version.
	Version string
	// FetchedAt is when the kept bundle was fetched.
	FetchedAt time.Time
	// CheckedAt is the last attempt, good or not; CheckedOKAt the last one that
	// was answered well, which stays when a later one fails.
	CheckedAt   time.Time
	CheckedOKAt time.Time
	// Error is the last failure in Caravana's words; empty when it worked.
	Error string
}

// TemplateCatalog returns the catalog row. It always exists: the migration
// makes it, with Checking on.
func (s *Store) TemplateCatalog(ctx context.Context) (TemplateCatalogRow, error) {
	var row TemplateCatalogRow
	var checking int
	var fetched, checked, checkedOK string
	err := s.db.QueryRowContext(ctx, `
		SELECT checking, bundle, bundle_sum, version, fetched_at, checked_at, checked_ok_at, error
		FROM template_catalog WHERE id = 1`).Scan(&checking, &row.Bundle, &row.BundleSum, &row.Version,
		&fetched, &checked, &checkedOK, &row.Error)
	if err != nil {
		return TemplateCatalogRow{}, fmt.Errorf("read template catalog: %w", err)
	}
	row.Checking = checking == 1
	if row.FetchedAt, err = parseTimeOrZero(fetched); err != nil {
		return TemplateCatalogRow{}, fmt.Errorf("read template catalog: %w", err)
	}
	if row.CheckedAt, err = parseTimeOrZero(checked); err != nil {
		return TemplateCatalogRow{}, fmt.Errorf("read template catalog: %w", err)
	}
	if row.CheckedOKAt, err = parseTimeOrZero(checkedOK); err != nil {
		return TemplateCatalogRow{}, fmt.Errorf("read template catalog: %w", err)
	}
	return row, nil
}

// SaveTemplateCheck stores what a check found: every column but the owner's
// setting, which only SetTemplateChecking changes, so a setting saved while a
// check is running isn't undone by it.
func (s *Store) SaveTemplateCheck(ctx context.Context, row TemplateCatalogRow) error {
	var bundle any // NULL when there is none
	if len(row.Bundle) > 0 {
		bundle = row.Bundle
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE template_catalog SET bundle = ?, bundle_sum = ?, version = ?, fetched_at = ?,
			checked_at = ?, checked_ok_at = ?, error = ?
		WHERE id = 1`,
		bundle, row.BundleSum, row.Version, zeroOrTime(row.FetchedAt),
		zeroOrTime(row.CheckedAt), zeroOrTime(row.CheckedOKAt), row.Error); err != nil {
		return fmt.Errorf("save template catalog: %w", err)
	}
	return nil
}

// SetTemplateChecking stores the owner's setting and touches nothing else.
func (s *Store) SetTemplateChecking(ctx context.Context, checking bool) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE template_catalog SET checking = ? WHERE id = 1", boolInt(checking)); err != nil {
		return fmt.Errorf("set template checking: %w", err)
	}
	return nil
}
