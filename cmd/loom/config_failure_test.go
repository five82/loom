package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIStateDirectoryFailures(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config-home"))
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "loom.toml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("[paths]\nstate_dir = %q\n", filepath.Join(blocked, "state"))), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"start"}, {"restart"}, {"migrate"}, {"backup"}} {
		_, err := runCLI(t, cfg, args...)
		if err == nil {
			t.Errorf("%v unexpectedly succeeded", args)
		}
	}
}

func TestCLIServiceInstallRequiresFFprobe(t *testing.T) {
	_, cfg := testCLI(t)
	path := fakeCLIService(t, "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, cfg, "service", "install")
	if err == nil || !strings.Contains(err.Error(), "ffprobe was not found") {
		t.Fatalf("install: %q, %v", output, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit created without ffprobe: %v", err)
	}
}
