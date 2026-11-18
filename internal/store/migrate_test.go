package store

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadMigrations_OrderAndParsing(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0010_later.sql":  {Data: []byte("SELECT 10;")},
		"m/0002_second.sql": {Data: []byte("SELECT 2;")},
		"m/0001_first.sql":  {Data: []byte("SELECT 1;")},
		"m/README.md":       {Data: []byte("ignored")},
	}
	got, err := loadMigrations(fsys, "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Version != 1 || got[1].Version != 2 || got[2].Version != 10 {
		t.Fatalf("wrong order: %+v", got)
	}
	if got[0].Name != "first" || got[0].SQL != "SELECT 1;" {
		t.Errorf("wrong parse: %+v", got[0])
	}
}

func TestLoadMigrations_Errors(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"bad name":  {"m/first.sql": {Data: []byte("x")}},
		"zero":      {"m/0000_x.sql": {Data: []byte("x")}},
		"duplicate": {"m/0001_a.sql": {Data: []byte("x")}, "m/0001_b.sql": {Data: []byte("x")}},
	}
	for name, fsys := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadMigrations(fsys, "m"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestEmbeddedMigrationsLoad(t *testing.T) {
	migs, err := loadMigrations(migrationFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) == 0 || migs[0].Version != 1 {
		t.Fatalf("embedded migrations: %+v", migs)
	}
	for _, table := range []string{"users", "roles", "role_permissions", "tags", "user_tags", "otp_challenges", "audit_log"} {
		if !strings.Contains(migs[0].SQL, "CREATE TABLE "+table+" ") && !strings.Contains(migs[0].SQL, "CREATE TABLE "+table+"\n") {
			t.Errorf("0001 does not create %s", table)
		}
	}
}
