package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// LoadDotEnv reads KEY=VALUE lines from the file at path into the process
// environment. It is meant for local development. A missing file is not an
// error, and variables that are already set in the environment are never
// overwritten, so real environment variables always win.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	vars, err := parseDotEnv(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for key, val := range vars {
		if _, alreadySet := os.LookupEnv(key); alreadySet {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("%s: setting %s: %w", path, key, err)
		}
	}
	return nil
}

// parseDotEnv understands: blank lines, # comments, an optional "export "
// prefix, and values wrapped in single or double quotes. It also tolerates
// the UTF-8 BOM and CRLF line endings that Windows editors produce.
func parseDotEnv(r io.Reader) (map[string]string, error) {
	vars := make(map[string]string)
	sc := bufio.NewScanner(r)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := sc.Text()
		if lineNo == 1 {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNo)
		}
		vars[key] = unquote(strings.TrimSpace(val))
	}
	return vars, sc.Err()
}

func unquote(v string) string {
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if first == last && (first == '"' || first == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
