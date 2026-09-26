package systemd

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestUninstallReportsReloadFailureAfterRemovingUnit(t *testing.T) {
	logPath := installFakeSystemctl(t, "")
	path, err := Install(context.Background(), "/opt/loom", "/home/config.toml", "/usr/bin/ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAIL_SYSTEMCTL_ARGS", "--user daemon-reload")
	if err := Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "requested failure") {
		t.Fatalf("Uninstall = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit should already be removed: %v", err)
	}
	assertCommandLog(t, logPath, "--user disable --now loom.service\n--user daemon-reload\n")
}
