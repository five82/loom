package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeCLIService(t *testing.T, fail string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "systemctl.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\nif [ \"$*\" = %q ]; then echo failed >&2; exit 1; fi\n", log, fail)
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	path := filepath.Join(root, "config", "systemd", "user", "loom.service")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Installed by `loom service install`.\n[Service]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIServiceLifecycleErrors(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		fail, want string
	}{
		{[]string{"service", "install"}, "", "already installed"},
		{[]string{"start"}, "--user start loom.service", "failed"},
		{[]string{"stop"}, "--user stop loom.service", "failed"},
		{[]string{"stop"}, "", "Daemon is not running"},
		{[]string{"restart"}, "--user restart loom.service", "failed"},
		{[]string{"service", "uninstall"}, "--user disable --now loom.service", "failed"},
		{[]string{"service", "uninstall"}, "", "service uninstalled"},
	} {
		t.Run(strings.Join(tc.args, " ")+tc.fail, func(t *testing.T) {
			_, cfg := testCLI(t)
			path := fakeCLIService(t, tc.fail)
			output, err := runCLI(t, cfg, tc.args...)
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("output %q, error %v", output, err)
				}
				if _, statErr := os.Stat(path); statErr != nil {
					t.Fatalf("unit removed after failure: %v", statErr)
				}
			} else if err != nil || !strings.Contains(output, tc.want) {
				t.Fatalf("output %q, error %v", output, err)
			}
		})
	}
}

func TestCLIServiceInstallEnablesBeforeStarting(t *testing.T) {
	_, cfg := testCLI(t)
	path := fakeCLIService(t, "--user start loom.service")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ffprobe := filepath.Join(os.Getenv("PATH"), "ffprobe")
	if err := os.WriteFile(ffprobe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, cfg, "service", "install")
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("install: %q, %v", output, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), cfg) {
		t.Fatalf("installed unit omitted config: %s", data)
	}
}

func TestCLIBackupMigrateAndAuditErrors(t *testing.T) {
	dir, cfg := testCLI(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config-home"))
	db := filepath.Join(dir, "state", "loom.db")
	// An existing non-database cannot be migrated or audited.
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"migrate"}, {"developer", "audit"}, {"backup", filepath.Join(dir, "snapshot.db")}} {
		if output, err := runCLI(t, cfg, args...); err == nil {
			t.Errorf("%v unexpectedly succeeded: %s", args, output)
		}
	}
}
