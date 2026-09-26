package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReportsUnreadableDefaultConfigCandidate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	// Reading a directory as a config file is an error, not a missing config
	// that should silently fall back to defaults.
	path := filepath.Join(root, "loom", "config.toml")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("directory at default config path = %v", err)
	}
}
