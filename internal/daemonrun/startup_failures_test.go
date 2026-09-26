package daemonrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReportsStateLockLogAndSocketFailures(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(*testing.T, string, string)
	}{
		{"state directory", "create state directory", func(t *testing.T, state, runtime string) {
			if err := os.WriteFile(state, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"daemon lock", "acquire daemon lock", func(t *testing.T, state, runtime string) {
			if err := os.WriteFile(runtime, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"daemon log", "open daemon log", func(t *testing.T, state, runtime string) {
			if err := os.MkdirAll(filepath.Join(state, "daemon.log"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"socket", "listen on daemon socket", func(t *testing.T, state, runtime string) {
			path := filepath.Join(runtime, "loom.sock")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testRunConfig(t)
			root := t.TempDir()
			runtime := filepath.Join(root, "runtime")
			if tc.name != "daemon lock" {
				if err := os.Mkdir(runtime, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("XDG_RUNTIME_DIR", runtime)
			tc.setup(t, cfg.Paths.StateDir, runtime)
			if err := Run(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run = %v", err)
			}
		})
	}
}
