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
