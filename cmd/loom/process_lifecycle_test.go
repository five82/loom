package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/five82/loom/internal/daemonctl"
)

func TestMain(m *testing.M) {
	if os.Getenv("LOOM_TEST_CHILD_DAEMON") == "exit" {
		os.Exit(23)
	}
	if os.Getenv("LOOM_TEST_CHILD_DAEMON") == "1" {
		os.Args = []string{"loom", "--config", os.Getenv("LOOM_TEST_CHILD_CONFIG"), "daemon"}
		main()
		return
	}
	os.Exit(m.Run())
}

func TestCLIStandaloneDaemonExitsDuringStartup(t *testing.T) {
	dir, cfg := testCLI(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-systemd"))
	t.Setenv("LOOM_TEST_CHILD_DAEMON", "exit")
	for _, command := range []string{"start", "restart"} {
		if _, err := runCLI(t, cfg, command); err == nil || !strings.Contains(err.Error(), "exited during startup") {
			t.Fatalf("%s error = %v", command, err)
		}
	}
}

func TestCLIStandaloneDaemonLifecycle(t *testing.T) {
	dir, cfg := testCLI(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "no-systemd"))
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("[api]\nbind = %q\n[paths]\nstate_dir = %q\n[scanner]\ninterval = %q\n", "127.0.0.1:0", filepath.Join(dir, "state"), "0")), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ffprobe"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOOM_TEST_CHILD_DAEMON", "1")
	t.Setenv("LOOM_TEST_CHILD_CONFIG", cfg)
	t.Cleanup(func() {
		if daemonctl.IsRunning(filepath.Join(dir, "loom.lock"), filepath.Join(dir, "loom.sock")) {
			_ = daemonctl.Stop(filepath.Join(dir, "loom.lock"), filepath.Join(dir, "loom.sock"))
		}
	})
	for _, tc := range []struct{ command, want string }{
		{"start", "Daemon started"},
		{"start", "Daemon already running"},
		{"restart", "Daemon restarted"},
		{"stop", "Daemon stopped"},
		{"stop", "Daemon is not running"},
	} {
		output, err := runCLI(t, cfg, tc.command)
		if err != nil || !strings.Contains(output, tc.want) {
			t.Fatalf("%s: output %q, err %v", tc.command, output, err)
		}
	}
}
