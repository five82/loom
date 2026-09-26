package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIReportsUnreadableSystemdPath(t *testing.T) {
	_, cfg := testCLI(t)
	fakeCLIService(t, "")
	home := os.Getenv("XDG_CONFIG_HOME")
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"start"}, {"stop"}, {"restart"}, {"service", "install"}, {"service", "uninstall"}} {
		output, err := runCLI(t, cfg, args...)
		if err == nil || !strings.Contains(err.Error(), "check systemd service") {
			t.Fatalf("%v: output %q, err %v", args, output, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "systemd", "user", "loom.service")); err == nil {
		t.Fatal("invalid unit path became accessible")
	}
}
