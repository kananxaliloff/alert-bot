// Package config loads the application configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultPollInterval = 5 * time.Minute
	minPollInterval     = 10 * time.Second
)

// Config holds all settings the application needs at startup.
type Config struct {
	// TelegramToken is the bot token from BotFather. Required.
	TelegramToken string
	// DatabaseURL is the PostgreSQL connection string. Required.
	DatabaseURL string
	// PollInterval is how often the pollers check ArgoCD and Gravitee.
	PollInterval time.Duration
}

// Load reads the configuration from the process environment.
// It reports every problem at once, so one run is enough to fix them all.
func Load() (Config, error) {
	return load(os.Getenv)
}

// load is Load with the environment lookup injected, which keeps it easy to test.
func load(getenv func(string) string) (Config, error) {
	var cfg Config
	var problems []error

	cfg.TelegramToken = strings.TrimSpace(getenv("TELEGRAM_TOKEN"))
	if cfg.TelegramToken == "" {
		problems = append(problems, errors.New("TELEGRAM_TOKEN is required"))
	}

	cfg.DatabaseURL = strings.TrimSpace(getenv("DATABASE_URL"))
	if cfg.DatabaseURL == "" {
		problems = append(problems, errors.New("DATABASE_URL is required"))
	}

	cfg.PollInterval = defaultPollInterval
	if raw := strings.TrimSpace(getenv("POLL_INTERVAL")); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			problems = append(problems,
				fmt.Errorf("POLL_INTERVAL %q is not a valid duration (examples: 30s, 5m, 1h)", raw))
		case d < minPollInterval:
			problems = append(problems,
				fmt.Errorf("POLL_INTERVAL %s is too small, the minimum is %s", d, minPollInterval))
		default:
			cfg.PollInterval = d
		}
	}

	if err := errors.Join(problems...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

// String returns a form that is safe to print: secrets are masked.
func (c Config) String() string {
	return fmt.Sprintf("Config{TelegramToken: %s, DatabaseURL: %s, PollInterval: %s}",
		mask(c.TelegramToken), maskURLPassword(c.DatabaseURL), c.PollInterval)
}

// LogValue makes slog print a masked Config, so logging it can never leak secrets.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("telegram_token", mask(c.TelegramToken)),
		slog.String("database_url", maskURLPassword(c.DatabaseURL)),
		slog.Duration("poll_interval", c.PollInterval),
	)
}

func mask(s string) string {
	if s == "" {
		return "(empty)"
	}
	return "***"
}

// maskURLPassword drops the user's password from a connection URL.
func maskURLPassword(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "***"
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	return u.String()
}
