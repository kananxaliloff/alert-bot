// Package directory answers one question: is this email an active employee,
// and what are their name and job title? Implementations: a JSON file for
// development and tests, and Active Directory over LDAP (package ldapdir).
package directory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrNotFound means there is no enabled account with that email.
var ErrNotFound = errors.New("directory: no active employee with that email")

// Person is what the bot needs to know about an employee.
type Person struct {
	Email    string
	FullName string
	JobTitle string
}

// Directory looks employees up by work email.
// Disabled accounts must be reported as ErrNotFound.
type Directory interface {
	LookupByEmail(ctx context.Context, email string) (Person, error)
}

// FileDirectory reads employees from a JSON file. The file is re-read on every
// lookup, so it can be edited while the bot is running. Intended for
// development and tests, not for production.
//
// Format:
//
//	[{"email": "ann@corp.example", "name": "Ann Example", "title": "Backend Engineer"},
//	 {"email": "old@corp.example", "name": "Gone Person", "title": "QA", "disabled": true}]
type FileDirectory struct {
	path string
}

// NewFile returns a directory backed by the JSON file at path. It checks
// that the file can be read and parsed right away.
func NewFile(path string) (*FileDirectory, error) {
	d := &FileDirectory{path: path}
	if _, err := d.load(); err != nil {
		return nil, err
	}
	return d, nil
}

type fileEntry struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	Disabled bool   `json:"disabled"`
}

func (d *FileDirectory) load() ([]fileEntry, error) {
	raw, err := os.ReadFile(d.path)
	if err != nil {
		return nil, fmt.Errorf("directory file: %w", err)
	}
	var entries []fileEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("directory file %s: %w", d.path, err)
	}
	return entries, nil
}

// LookupByEmail implements Directory.
func (d *FileDirectory) LookupByEmail(_ context.Context, email string) (Person, error) {
	entries, err := d.load()
	if err != nil {
		return Person{}, err
	}
	want := strings.ToLower(strings.TrimSpace(email))
	for _, e := range entries {
		if strings.ToLower(strings.TrimSpace(e.Email)) == want && !e.Disabled {
			return Person{Email: want, FullName: e.Name, JobTitle: e.Title}, nil
		}
	}
	return Person{}, ErrNotFound
}

// EscapeFilter escapes a value for use inside an LDAP search filter (RFC 4515).
func EscapeFilter(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\5c`)
		case '*':
			b.WriteString(`\2a`)
		case '(':
			b.WriteString(`\28`)
		case ')':
			b.WriteString(`\29`)
		case 0:
			b.WriteString(`\00`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ADFilter builds the Active Directory filter that finds one enabled user
// account by its mail attribute. The userAccountControl clause (matching rule
// 1.2.840.113556.1.4.803, bit 2) excludes disabled accounts.
func ADFilter(email string) string {
	return "(&(objectCategory=person)(objectClass=user)(mail=" + EscapeFilter(email) + ")" +
		"(!(userAccountControl:1.2.840.113556.1.4.803:=2)))"
}
