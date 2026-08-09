package systemd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallWritesAndEnablesUserUnit(t *testing.T) {
	logPath := installFakeSystemctl(t, "")

	path, err := Install(context.Background(), "/opt/loom/bin/loom", "/home/media/config.toml", "/opt/ffmpeg/bin/ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd", "user", unitName)
	if path != wantPath {
		t.Fatalf("unit path = %q, want %q", path, wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(data)
	for _, want := range []string{
		unitMarker,
		`ExecStart="/opt/loom/bin/loom" --config "/home/media/config.toml" daemon`,
		`Environment="PATH=/opt/ffmpeg/bin:/usr/local/bin:/usr/bin:/bin"`,
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit does not contain %q:\n%s", want, unit)
		}
	}
	assertCommandLog(t, logPath, "--user daemon-reload\n--user enable loom.service\n")

	installed, err := Installed()
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Fatal("Installed reported false after installation")
	}
}

func TestInstallQuotesSystemdSpecialCharacters(t *testing.T) {
	unit, err := renderUnit(`/opt/loom 100%/loom$`, `/home/user/a "config".toml`, `/opt/ffmpeg$/ffprobe`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, `ExecStart="/opt/loom 100%%/loom$$" --config "/home/user/a \"config\".toml" daemon`) {
		t.Fatalf("unit arguments were not quoted correctly:\n%s", unit)
	}
	if !strings.Contains(unit, `Environment="PATH=/opt/ffmpeg$:/usr/local/bin:/usr/bin:/bin"`) {
		t.Fatalf("unit environment was not quoted correctly:\n%s", unit)
	}
}

func TestLifecycleCommandsUseUserService(t *testing.T) {
	logPath := installFakeSystemctl(t, "")
	ctx := context.Background()
	if err := Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Restart(ctx); err != nil {
		t.Fatal(err)
	}
	assertCommandLog(t, logPath, "--user start loom.service\n--user stop loom.service\n--user restart loom.service\n")
}

func TestUninstallDisablesAndRemovesUserUnit(t *testing.T) {
	logPath := installFakeSystemctl(t, "")
	path, err := Install(context.Background(), "/opt/loom", "/home/media/config.toml", "/usr/bin/ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit still exists or stat failed: %v", err)
	}
	assertCommandLog(t, logPath, "--user disable --now loom.service\n--user daemon-reload\n")
}

func TestUninstallRefusesUnitNotCreatedByLoom(t *testing.T) {
	installFakeSystemctl(t, "")
	path, err := UnitPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Service]\nExecStart=/custom/loom\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(context.Background()); err == nil {
		t.Fatal("Uninstall removed an unmanaged unit")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unmanaged unit was removed: %v", err)
	}
}

func TestInstallRollsBackWhenEnableFails(t *testing.T) {
	installFakeSystemctl(t, "--user enable loom.service")
	path, err := UnitPath()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Install(context.Background(), "/opt/loom", "/home/media/config.toml", "/usr/bin/ffprobe"); err == nil {
		t.Fatal("Install succeeded when systemctl enable failed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit was not removed after failed installation: %v", err)
	}
}

func installFakeSystemctl(t *testing.T, failArgs string) string {
	t.Helper()
	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	binDir := filepath.Join(root, "bin")
	logPath := filepath.Join(root, "systemctl.log")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SYSTEMCTL_LOG"
if [ "$*" = "$FAIL_SYSTEMCTL_ARGS" ]; then
    echo "requested failure" >&2
    exit 1
fi
`
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("PATH", binDir)
	t.Setenv("SYSTEMCTL_LOG", logPath)
	t.Setenv("FAIL_SYSTEMCTL_ARGS", failArgs)
	return logPath
}

func assertCommandLog(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("systemctl commands:\n%s\nwant:\n%s", data, want)
	}
}
