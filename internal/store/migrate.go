package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// advisoryLockID serialises migrations when several instances start together.
const advisoryLockID = 727274001

type migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrate applies all pending SQL migrations in order. Each migration runs in
// its own transaction and is recorded in schema_migrations.
func Migrate(ctx context.Context, db *sql.DB) error {
	return migrate(ctx, db, migrationFS, "migrations")
}

func migrate(ctx context.Context, db *sql.DB, fsys fs.FS, dir string) error {
	migs, err := loadMigrations(fsys, dir)
	if err != nil {
		return err
	}

	// A dedicated connection keeps the advisory lock and the migrations together.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: connect: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(advisoryLockID)); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(advisoryLockID)) //nolint:errcheck

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INT PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("migrate: read applied: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("migrate: scan: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("migrate: read applied: %w", err)
	}
	rows.Close()

	for _, m := range migs {
		if applied[m.Version] {
			continue
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migrate %04d_%s: begin: %w", m.Version, m.Name, err)
		}
		// The file has several statements and no parameters; PostgreSQL drivers
		// run that through the simple protocol, which accepts multiple statements.
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("migrate %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.Version, m.Name); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("migrate %04d_%s: record: %w", m.Version, m.Name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migrate %04d_%s: commit: %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// loadMigrations reads NNNN_name.sql files, sorted by version.
func loadMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: read %s: %w", dir, err)
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		numPart, name, ok := strings.Cut(base, "_")
		version, convErr := strconv.Atoi(numPart)
		if !ok || convErr != nil || version <= 0 {
			return nil, fmt.Errorf("migrate: file %q must be named NNNN_description.sql", e.Name())
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrate: version %d used by both %q and %q", version, prev, e.Name())
		}
		seen[version] = e.Name()
		body, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", e.Name(), err)
		}
		out = append(out, migration{Version: version, Name: name, SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
