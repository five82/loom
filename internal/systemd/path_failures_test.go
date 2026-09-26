package systemd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitPathFallbackAndMissingHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())
	path, err := UnitPath()
	if err != nil || !strings.HasSuffix(path, filepath.Join("systemd", "user", "loom.service")) {
		t.Fatalf("unit path = %q, %v", path, err)
	}
	t.Setenv("HOME", "")
	if _, err := UnitPath(); err == nil || !strings.Contains(err.Error(), "resolve user config directory") {
		t.Fatalf("no home = %v", err)
	}
	if _, err := Installed(); err == nil {
		t.Fatal("Installed ignored missing home")
	}
	if _, err := Install(context.Background(), "/loom", "/config", "/ffprobe"); err == nil {
		t.Fatal("Install ignored missing home")
	}
	if err := Uninstall(context.Background()); err == nil {
		t.Fatal("Uninstall ignored missing home")
	}
}

func TestInstallFailsWhenUnitDirectoryIsFile(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.Mkdir(configHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "systemd"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), "/loom", "/config", "/ffprobe"); err == nil || !strings.Contains(err.Error(), "create systemd user directory") {
		t.Fatalf("install = %v", err)
	}
}

func TestUninstallRefusesForeignOrUnreadableUnit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "systemd", "user", "loom.service")
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(filepath.Dir(filepath.Dir(path))))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Service]\nExecStart=/other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "not installed by Loom") {
		t.Fatalf("foreign unit = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "read systemd service") {
		t.Fatalf("unreadable unit = %v", err)
	}
	if _, err := Installed(); err != nil {
		t.Fatalf("directory still exists: %v", err)
	}
}
