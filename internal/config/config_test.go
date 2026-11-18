package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// env returns a getenv function backed by a map, so tests never touch the real environment.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_Success(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"TELEGRAM_TOKEN": "123:abc",
		"DATABASE_URL":   "postgres://u:p@localhost:5432/db",
		"POLL_INTERVAL":  "30s",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TelegramToken != "123:abc" {
		t.Errorf("TelegramToken = %q", cfg.TelegramToken)
	}
	if cfg.DatabaseURL != "postgres://u:p@localhost:5432/db" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, want 30s", cfg.PollInterval)
	}
}

func TestLoad_DefaultPollInterval(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"TELEGRAM_TOKEN": "x",
		"DATABASE_URL":   "postgres://localhost/db",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PollInterval != 5*time.Minute {
		t.Errorf("PollInterval = %s, want 5m", cfg.PollInterval)
	}
}

func TestLoad_ReportsAllMissingAtOnce(t *testing.T) {
	_, err := load(env(nil))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"TELEGRAM_TOKEN", "DATABASE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoad_WhitespaceOnlyCountsAsMissing(t *testing.T) {
	_, err := load(env(map[string]string{
		"TELEGRAM_TOKEN": "   ",
		"DATABASE_URL":   "postgres://localhost/db",
	}))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_TOKEN") {
		t.Fatalf("expected TELEGRAM_TOKEN error, got %v", err)
	}
}

func TestLoad_BadPollInterval(t *testing.T) {
	cases := map[string]string{
		"not a duration": "soon",
		"too small":      "1s",
		"negative":       "-5m",
		"missing unit":   "300",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := load(env(map[string]string{
				"TELEGRAM_TOKEN": "x",
				"DATABASE_URL":   "postgres://localhost/db",
				"POLL_INTERVAL":  value,
			}))
			if err == nil || !strings.Contains(err.Error(), "POLL_INTERVAL") {
				t.Fatalf("expected POLL_INTERVAL error for %q, got %v", value, err)
			}
		})
	}
}

func TestConfig_NeverPrintsSecrets(t *testing.T) {
	cfg := Config{
		TelegramToken: "super-secret-token",
		DatabaseURL:   "postgres://alertbot:db-password@localhost:5432/alertbot",
		PollInterval:  time.Minute,
	}

	var out strings.Builder
	log := slog.New(slog.NewTextHandler(&out, nil))
	log.Info("cfg", "config", cfg)

	for name, text := range map[string]string{
		"String":  cfg.String(),
		"%v":      fmt.Sprintf("%v", cfg),
		"slog":    out.String(),
		"%+v":     fmt.Sprintf("%+v", cfg),
		"Sprint":  fmt.Sprint(cfg),
		"Sprintf": fmt.Sprintf("%s", cfg),
	} {
		for _, secret := range []string{"super-secret-token", "db-password"} {
			if strings.Contains(text, secret) {
				t.Errorf("%s output leaks %q: %s", name, secret, text)
			}
		}
	}
	if !strings.Contains(cfg.String(), "alertbot@localhost:5432") {
		t.Errorf("masked URL should keep user and host, got %s", cfg.String())
	}
}
