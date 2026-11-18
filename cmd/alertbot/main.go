// Command alertbot is the entry point of the alert bot.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/kananxaliloff/alert-bot/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// Local development convenience: load .env if it exists.
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("alert-bot starting", "config", cfg)
	return nil
}
