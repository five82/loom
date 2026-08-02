package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadExplicitConfig(t *testing.T) {
	t.Setenv("TMDB_API_KEY", "environment-key")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := `[api]
bind = "127.0.0.1:9000"
[paths]
state_dir = "state"
[library]
movies_dir = "movies"
tv_dir = "tv"
[scanner]
interval = "6h"
[tmdb]
api_key = "file-key"
language = "en-US"
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourcePath != path {
		t.Fatalf("SourcePath = %q, want %q", cfg.SourcePath, path)
	}
	if cfg.TMDB.APIKey != "environment-key" {
		t.Fatalf("API key = %q, want environment override", cfg.TMDB.APIKey)
	}
	if !filepath.IsAbs(cfg.Library.MoviesDir) || !filepath.IsAbs(cfg.Paths.StateDir) {
		t.Fatal("paths were not normalized to absolute paths")
	}
	interval, err := cfg.ScanInterval()
	if err != nil {
		t.Fatal(err)
	}
	if interval != 6*time.Hour {
		t.Fatalf("interval = %v, want 6h", interval)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("unknown = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted an unknown field")
	}
}

func TestDefaultAPIDoesNotConflictWithJellyfin(t *testing.T) {
	cfg := defaultConfig()
	if cfg.API.Bind != "0.0.0.0:8097" {
		t.Fatalf("default API bind = %q, want Loom port 8097", cfg.API.Bind)
	}
}

func TestScanIntervalCanBeDisabled(t *testing.T) {
	cfg := defaultConfig()
	cfg.Scanner.Interval = "0"
	got, err := cfg.ScanInterval()
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("interval = %v, want zero", got)
	}
}

func TestResetStateRemovesStateAndPreservesExternalConfig(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	configPath := filepath.Join(root, "config.toml")
	writeTestFile(t, configPath, "config")
	writeTestFile(t, filepath.Join(stateDir, "loom.db"), "database")
	writeTestFile(t, filepath.Join(stateDir, "images", "poster.jpg"), "artwork")
	writeTestFile(t, filepath.Join(stateDir, "daemon.log"), "log")

	cfg := defaultConfig()
	cfg.SourcePath = configPath
	cfg.Paths.StateDir = stateDir
	if err := cfg.ResetState(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("state directory still exists or stat failed: %v", err)
	}
	assertTestFile(t, configPath, "config")
}

func TestResetStatePreservesConfigInsideStateDirectory(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	configPath := filepath.Join(stateDir, "settings", "config.toml")
	writeTestFile(t, configPath, "config")
	writeTestFile(t, filepath.Join(stateDir, "settings", "other.toml"), "other")
	writeTestFile(t, filepath.Join(stateDir, "loom.db"), "database")
	writeTestFile(t, filepath.Join(stateDir, "images", "poster.jpg"), "artwork")

	cfg := defaultConfig()
	cfg.SourcePath = configPath
	cfg.Paths.StateDir = stateDir
	if err := cfg.ResetState(); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, configPath, "config")
	for _, path := range []string{
		filepath.Join(stateDir, "settings", "other.toml"),
		filepath.Join(stateDir, "loom.db"),
		filepath.Join(stateDir, "images"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("state path %q still exists or stat failed: %v", path, err)
		}
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("file %q = %q, want %q", path, contents, want)
	}
}
