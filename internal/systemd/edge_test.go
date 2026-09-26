package systemd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLingerEnabled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	for _, tc := range []struct {
		output  string
		want    bool
		wantErr string
	}{
		{"yes", true, ""}, {"no", false, ""}, {"maybe", false, "unexpected linger value"},
	} {
		t.Run(tc.output, func(t *testing.T) {
			script := filepath.Join(root, "loginctl")
			if err := os.WriteFile(script, []byte("#!/bin/sh\necho "+tc.output+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			got, err := LingerEnabled(context.Background())
			if got != tc.want || (tc.wantErr == "" && err != nil) || (tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr))) {
				t.Fatalf("LingerEnabled = %v, %v", got, err)
			}
		})
	}
	if err := os.Remove(filepath.Join(root, "loginctl")); err != nil {
		t.Fatal(err)
	}
	if _, err := LingerEnabled(context.Background()); err == nil || !strings.Contains(err.Error(), "loginctl") {
		t.Fatalf("missing loginctl: %v", err)
	}
}

func TestInstallAndUninstallFailures(t *testing.T) {
	t.Run("existing unit", func(t *testing.T) {
		installFakeSystemctl(t, "")
		if _, err := Install(context.Background(), "/opt/loom", "/home/config.toml", "/usr/bin/ffprobe"); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(context.Background(), "/opt/loom", "/home/config.toml", "/usr/bin/ffprobe"); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("second install: %v", err)
		}
	})
	t.Run("config directory is a file", func(t *testing.T) {
		log := installFakeSystemctl(t, "")
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if err := os.WriteFile(configDir, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(context.Background(), "/opt/loom", "/home/config.toml", "/usr/bin/ffprobe"); err == nil || !strings.Contains(err.Error(), "create systemd user directory") {
			t.Fatalf("install with blocked config directory = %v", err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatalf("systemctl invoked: %v", err)
		}
	})
	t.Run("invalid executable", func(t *testing.T) {
		log := installFakeSystemctl(t, "")
		if _, err := Install(context.Background(), "", "/home/config.toml", "/usr/bin/ffprobe"); err == nil || !strings.Contains(err.Error(), "executable path is empty") {
			t.Fatalf("install: %v", err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatalf("systemctl invoked: %v", err)
		}
	})
	t.Run("reload failure", func(t *testing.T) {
		log := installFakeSystemctl(t, "--user daemon-reload")
		path, err := UnitPath()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Install(context.Background(), "/opt/loom", "/home/config.toml", "/usr/bin/ffprobe"); err == nil || !strings.Contains(err.Error(), "requested failure") {
			t.Fatalf("install: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unit after rollback: %v", err)
		}
		assertCommandLog(t, log, "--user daemon-reload\n--user disable loom.service\n--user daemon-reload\n")
	})
	t.Run("disable failure", func(t *testing.T) {
		log := installFakeSystemctl(t, "--user disable --now loom.service")
		path, err := Install(context.Background(), "/opt/loom", "/home/config.toml", "/usr/bin/ffprobe")
		if err != nil {
			t.Fatal(err)
		}
		if err := Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "requested failure") {
			t.Fatalf("uninstall: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unit removed on failure: %v", err)
		}
		assertCommandLog(t, log, "--user daemon-reload\n--user enable loom.service\n--user disable --now loom.service\n")
	})
	t.Run("absent unit", func(t *testing.T) {
		installFakeSystemctl(t, "")
		if err := Uninstall(context.Background()); err != nil {
			t.Fatal(err)
		}
		installed, err := Installed()
		if err != nil || installed {
			t.Fatalf("Installed = %v, %v", installed, err)
		}
	})
}

func TestRenderUnitRejectsUnsafePaths(t *testing.T) {
	for _, tc := range []struct{ executable, config, ffprobe, want string }{
		{"", "/config", "/ffprobe", "executable path is empty"},
		{"/loom", "", "/ffprobe", "config file path is empty"},
		{"/loom", "/config", "", "ffprobe executable path is empty"},
		{"/loom\nother", "/config", "/ffprobe", "unsupported character"},
	} {
		if _, err := renderUnit(tc.executable, tc.config, tc.ffprobe); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("renderUnit(%q, %q, %q): %v", tc.executable, tc.config, tc.ffprobe, err)
		}
	}
}
