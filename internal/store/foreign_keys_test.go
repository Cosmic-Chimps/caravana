package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Feature 054 turns SQLite's foreign key enforcement on (the DSN's foreign_keys(1)). Older
// migrations declared REFERENCES that SQLite never enforced, so a database written before may hold
// rows whose parent is gone. This test builds a database at migration 0042 with such an orphan in
// every table that declares a foreign key, opens it as Caravana does (which runs 0043 with
// enforcement on) and checks the store still works on it.

// fkChild is one declared foreign key of the schema at migration 0042.
type fkChild struct{ table, column, parent string }

// orphanChecks are the values CHECK constraints need in the rows a test seeds by hand.
var orphanChecks = map[string]map[string]string{
	"backup_copies":           {"status": "succeeded"},
	"backups":                 {"status": "failed"},
	"builds":                  {"status": "failed"},
	"deployments":             {"status": "failed"},
	"image_cleanups":          {"trigger": "owner", "status": "done"},
	"image_findings":          {"severity": "low"},
	"metrics_samples":         {"res": "60"},
	"notification_deliveries": {"status": "failed"},
	"restores":                {"status": "failed"},
	"schedule_runs":           {"status": "failed"},
	"server_moves":            {"state": "done"},
}

func TestOrphansFromBeforeForeignKeysAreKept(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := testDB(t, dir)
	if _, err := migrate(ctx, db, migrationsUpTo(t, 42)); err != nil {
		t.Fatal(err)
	}
	// What the old binary did: no enforcement. (database/sql keeps the one connection.)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}

	// Every foreign key the schema declares at 0042, found rather than listed, so a table that
	// gains one without this test being told still fails it.
	rows, err := db.QueryContext(ctx, `SELECT m.name, f."from", f."table" FROM sqlite_master m, pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	var fks []fkChild
	for rows.Next() {
		var c fkChild
		if err := rows.Scan(&c.table, &c.column, &c.parent); err != nil {
			t.Fatal(err)
		}
		fks = append(fks, c)
	}
	rows.Close()
	var tables []string
	for _, c := range fks {
		if !slices.Contains(tables, c.table) {
			tables = append(tables, c.table)
		}
	}
	if len(tables) != 15 {
		t.Fatalf("tables with a declared foreign key = %v, want the 15 this test was written for; extend it", tables)
	}

	// One orphan in each: its parent key names a row that doesn't exist.
	for _, table := range tables {
		cols, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT name, type, "notnull", dflt_value IS NOT NULL, pk FROM pragma_table_info('%s')`, table))
		if err != nil {
			t.Fatal(err)
		}
		var names, vals []string
		for cols.Next() {
			var name, typ string
			var notNull, hasDefault, pk int
			if err := cols.Scan(&name, &typ, &notNull, &hasDefault, &pk); err != nil {
				t.Fatal(err)
			}
			isFK := slices.ContainsFunc(fks, func(c fkChild) bool { return c.table == table && c.column == name })
			override, checked := orphanChecks[table][name]
			if !isFK && !checked && (pk == 0 && (notNull == 0 || hasDefault == 1)) {
				continue
			}
			names = append(names, name)
			switch {
			case checked:
				vals = append(vals, "'"+override+"'")
				if typ == "INTEGER" {
					vals[len(vals)-1] = override
				}
			case isFK && typ == "INTEGER":
				vals = append(vals, "987654")
			case isFK:
				vals = append(vals, "'no-such-parent'")
			case typ == "INTEGER":
				vals = append(vals, "1")
			case typ == "BLOB":
				vals = append(vals, "X'00'")
			default:
				vals = append(vals, "'orphan-"+table+"'")
			}
		}
		cols.Close()
		q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(names, ", "), strings.Join(vals, ", "))
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seeding an orphan in %s: %v\n%s", table, err, q)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Caravana opens it: 0043 runs with foreign keys enforced.
	st, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open of a database with orphans = %v", err)
	}
	defer st.Close()

	var violations int
	vrows, err := st.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	for vrows.Next() {
		violations++
	}
	vrows.Close()
	// The orphans are kept as they were (0043 repairs nothing and deletes nothing): one violation per
	// foreign key of each orphan, 16 for the 15 tables (metrics_samples has two keys, and
	// backup_copies' composite key counts once).
	if violations != 16 {
		t.Errorf("foreign_key_check after 0043 = %d violations, want 16 (every orphan kept)", violations)
	}
	for _, table := range tables {
		var n int
		if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_foreign_key_check('"+table+"')").Scan(&n); err != nil || n == 0 {
			t.Errorf("%s has %d orphans after 0043 (%v), want its one kept", table, n, err)
		}
	}

	// A normal write in each table: change a column that isn't a key, then remove the orphan.
	for _, table := range tables {
		var column string
		err := st.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT name FROM pragma_table_info('%s') WHERE pk = 0 AND "notnull" = 1 AND type = 'TEXT' LIMIT 1`, table)).Scan(&column)
		if err == nil && !slices.ContainsFunc(fks, func(c fkChild) bool { return c.table == table && c.column == column }) {
			if _, err := st.db.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET %s = %s", table, column, column)); err != nil {
				t.Errorf("UPDATE of an orphan in %s = %v", table, err)
			}
		}
		if _, err := st.db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Errorf("DELETE of an orphan in %s = %v", table, err)
		}
	}
}
