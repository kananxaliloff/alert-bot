//go:build integration

package store_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/kananxaliloff/alert-bot/internal/store"
	"github.com/kananxaliloff/alert-bot/internal/store/storetest"
)

// Run with:  go test -tags integration ./internal/store/...
// Needs TEST_DATABASE_URL pointing at a database whose name ends in "_test".
// The tables are emptied before every test, so the name check is a safety net.
func TestPostgresStoreContract(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasSuffix(strings.TrimPrefix(u.Path, "/"), "_test") {
		t.Fatalf("refusing to run: database name in TEST_DATABASE_URL must end in _test (got path %q)", u.Path)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("cannot reach database: %v", err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Running Migrate twice must be harmless.
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	storetest.Run(t, func(t *testing.T) store.Store {
		t.Helper()
		_, err := db.ExecContext(ctx, `TRUNCATE audit_log, otp_challenges, user_tags, users, tags, role_permissions, roles RESTART IDENTITY CASCADE`)
		if err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return store.NewPostgres(db)
	})
}
