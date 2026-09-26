package systemd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledReportsInaccessibleConfigPath(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "config")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", file)
	if installed, err := Installed(); installed || err == nil || !strings.Contains(err.Error(), "check systemd service") {
		t.Fatalf("Installed = %v, %v", installed, err)
	}
}
