package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReportsBadDefaultPathAndNormalizationErrors(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	if err := os.MkdirAll(filepath.Join(root, "loom", "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("config path is directory: %v", err)
	}
	explicit := filepath.Join(root, "explicit.toml")
	if err := os.WriteFile(explicit, []byte("[paths]\nstate_dir = '~/state'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	if _, err := Load(explicit); err == nil || !strings.Contains(err.Error(), "resolve home directory") {
		t.Fatalf("no home for tilde: %v", err)
	}
	if err := os.WriteFile(explicit, []byte("name = ''\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(explicit); err == nil || !strings.Contains(err.Error(), "validate config") {
		t.Fatalf("invalid name: %v", err)
	}
	cfg, err := Load("")
	if err == nil || cfg != nil {
		t.Fatalf("broken default config path accepted: %+v, %v", cfg, err)
	}
}

func TestDefaultConfigFallsBackWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	if got := defaultConfig().Paths.StateDir; got != "/.local/state/loom" {
		t.Fatalf("state directory without home = %q", got)
	}
}
