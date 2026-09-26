package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLICommandsReportInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config-home"))
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("unknown_field = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"start"}, {"stop"}, {"restart"}, {"status"}, {"scan"}, {"unmatched"},
		{"search", "movie", "title"}, {"match", "1", "2"}, {"backup"}, {"migrate"},
		{"config", "validate"}, {"developer", "audit"}, {"developer", "reset"}, {"logs"},
	} {
		_, err := runCLI(t, cfg, args...)
		if err == nil || !strings.Contains(err.Error(), "parse config") {
			t.Errorf("%v: %v", args, err)
		}
	}
}
