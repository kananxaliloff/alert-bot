package directory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "directory.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const sample = `[
  {"email": "Ann@Corp.Example", "name": "Ann Example", "title": "Backend Engineer"},
  {"email": "gone@corp.example", "name": "Gone Person", "title": "QA", "disabled": true}
]`

func TestFileDirectory_Lookup(t *testing.T) {
	d, err := NewFile(writeFile(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	p, err := d.LookupByEmail(ctx, "  ann@corp.example ")
	if err != nil {
		t.Fatal(err)
	}
	if p.FullName != "Ann Example" || p.JobTitle != "Backend Engineer" || p.Email != "ann@corp.example" {
		t.Errorf("person = %+v", p)
	}
	if _, err := d.LookupByEmail(ctx, "ANN@CORP.EXAMPLE"); err != nil {
		t.Errorf("lookup must be case-insensitive: %v", err)
	}
	if _, err := d.LookupByEmail(ctx, "gone@corp.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("disabled account must be ErrNotFound, got %v", err)
	}
	if _, err := d.LookupByEmail(ctx, "nobody@corp.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown must be ErrNotFound, got %v", err)
	}
}

func TestFileDirectory_RereadsFile(t *testing.T) {
	path := writeFile(t, `[]`)
	d, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.LookupByEmail(context.Background(), "new@corp.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := os.WriteFile(path, []byte(`[{"email":"new@corp.example","name":"New","title":"Dev"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.LookupByEmail(context.Background(), "new@corp.example"); err != nil {
		t.Fatalf("edited file should be picked up: %v", err)
	}
}

func TestNewFile_Errors(t *testing.T) {
	if _, err := NewFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing file should fail at startup")
	}
	if _, err := NewFile(writeFile(t, `not json`)); err == nil {
		t.Error("invalid JSON should fail at startup")
	}
}

func TestEscapeFilter(t *testing.T) {
	cases := map[string]string{
		"plain@corp.example": "plain@corp.example",
		"a*b":                `a\2ab`,
		"a(b)c":              `a\28b\29c`,
		`a\b`:                `a\5cb`,
		"a\x00b":             `a\00b`,
		"*)(uid=*":           `\2a\29\28uid=\2a`,
	}
	for in, want := range cases {
		if got := EscapeFilter(in); got != want {
			t.Errorf("EscapeFilter(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestADFilter_CannotBeInjected(t *testing.T) {
	f := ADFilter("x)(objectClass=*")
	if strings.Contains(f, "x)(objectClass=*") {
		t.Fatalf("filter injection possible: %s", f)
	}
	want := "(&(objectCategory=person)(objectClass=user)(mail=ann@corp.example)(!(userAccountControl:1.2.840.113556.1.4.803:=2)))"
	if got := ADFilter("ann@corp.example"); got != want {
		t.Errorf("ADFilter = %s", got)
	}
}
