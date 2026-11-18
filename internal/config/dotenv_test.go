package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	input := "\xef\xbb\xbf# comment\r\n" +
		"\r\n" +
		"A=1\r\n" +
		"export B=two words\r\n" +
		"C=\"quoted value\"\r\n" +
		"D='single'\r\n" +
		"E=a=b=c\r\n" +
		"  F  =  spaced  \r\n" +
		"G=\r\n"

	got, err := parseDotEnv(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"A": "1",
		"B": "two words",
		"C": "quoted value",
		"D": "single",
		"E": "a=b=c",
		"F": "spaced",
		"G": "",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d vars %v, want %d", len(got), got, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseDotEnv_BadLine(t *testing.T) {
	_, err := parseDotEnv(strings.NewReader("A=1\nthis is not valid\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected error naming line 2, got %v", err)
	}
}

func TestLoadDotEnv_MissingFileIsFine(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatalf("missing file should not be an error: %v", err)
	}
}

func TestLoadDotEnv_DoesNotOverrideRealEnvironment(t *testing.T) {
	t.Setenv("ALERTBOT_TEST_KEEP", "from-environment")
	t.Setenv("ALERTBOT_TEST_NEW", "") // registers cleanup, then we unset it below
	os.Unsetenv("ALERTBOT_TEST_NEW")

	path := filepath.Join(t.TempDir(), ".env")
	content := "ALERTBOT_TEST_KEEP=from-file\nALERTBOT_TEST_NEW=from-file\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("ALERTBOT_TEST_KEEP"); got != "from-environment" {
		t.Errorf("existing variable was overridden: %q", got)
	}
	if got := os.Getenv("ALERTBOT_TEST_NEW"); got != "from-file" {
		t.Errorf("new variable not loaded: %q", got)
	}
}
