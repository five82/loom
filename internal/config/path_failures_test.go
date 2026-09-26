package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPathsWhenHomeIsUnavailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if _, err := DefaultPath(); err == nil || !strings.Contains(err.Error(), "user config directory") {
		t.Fatalf("default path = %v", err)
	}
	if _, err := absolutePath("~/movies"); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("tilde expansion = %v", err)
	}
	if _, err := WriteSample(""); err == nil {
		t.Fatal("sample accepted missing home")
	}
}

func TestRuntimeFallbackAndStateImageDirectoryError(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if runtimeDir() != "/tmp" {
		t.Fatalf("runtime fallback = %q", runtimeDir())
	}
	root := t.TempDir()
	cfg := &Config{Paths: PathsConfig{StateDir: filepath.Join(root, "state")}}
	if err := os.Mkdir(cfg.Paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ImageDir(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureStateDir(); err == nil || !strings.Contains(err.Error(), "create state directory") {
		t.Fatalf("image directory failure = %v", err)
	}
	if err := removeAllExcept(cfg.ImageDir(), filepath.Join(cfg.ImageDir(), "config.toml")); err == nil || !strings.Contains(err.Error(), "read state directory") {
		t.Fatalf("file used as state directory = %v", err)
	}
	if pathContains("relative", "/absolute") {
		t.Fatal("relative directory contains absolute path")
	}
}

func TestWriteSampleDirectoryAndExistingFileErrors(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSample(filepath.Join(file, "config.toml")); err == nil || !strings.Contains(err.Error(), "create config directory") {
		t.Fatalf("directory error = %v", err)
	}
	if _, err := WriteSample(file); err == nil || !strings.Contains(err.Error(), "create config") {
		t.Fatalf("existing file error = %v", err)
	}
}
