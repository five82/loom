package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSearchOrderAndDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", root)
	t.Setenv("TMDB_API_KEY", "from-env")
	dir := filepath.Join(root, "work")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	cfg, err := Load("")
	if err != nil || cfg.SourcePath != "" || cfg.TMDB.APIKey != "from-env" {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	local := filepath.Join(dir, "loom.toml")
	writeTestFile(t, local, "name = \"Local\"\n")
	cfg, err = Load("")
	if err != nil || cfg.SourcePath != local || cfg.Name != "Local" {
		t.Fatalf("local: %+v %v", cfg, err)
	}
	xdg := filepath.Join(root, "config", "loom", "config.toml")
	writeTestFile(t, xdg, "name = \"XDG\"\n")
	cfg, err = Load("")
	if err != nil || cfg.SourcePath != xdg || cfg.Name != "XDG" {
		t.Fatalf("XDG: %+v %v", cfg, err)
	}
	cfg, err = Load(local)
	if err != nil || cfg.SourcePath != local || cfg.Name != "Local" {
		t.Fatalf("explicit: %+v %v", cfg, err)
	}
	path, err := DefaultPath()
	if err != nil || path != xdg {
		t.Fatalf("DefaultPath = %q, %v", path, err)
	}
}

func TestConfigReadFailuresAndSampleDefault(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	path, err := WriteSample("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "config", "loom", "config.toml")
	if path != want {
		t.Fatalf("sample at %q, want %q", path, want)
	}
	if _, err := Load(filepath.Join(root, "missing")); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("missing explicit config: %v", err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("directory as config: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("unreadable XDG config: %v", err)
	}
	if _, err := WriteSample(path); err == nil || !strings.Contains(err.Error(), "create config") {
		t.Fatalf("sample over directory: %v", err)
	}
}
