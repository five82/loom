package systemd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	unitName   = "loom.service"
	unitMarker = "# Installed by `loom service install`."
)

// UnitPath returns the path used for Loom's per-user systemd unit.
func UnitPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(configDir, "systemd", "user", unitName), nil
}

// Installed reports whether a per-user Loom unit exists.
func Installed() (bool, error) {
	path, err := UnitPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("check systemd service: %w", err)
}

// Install writes and enables a per-user Loom unit without starting it.
func Install(ctx context.Context, executable, configPath, ffprobePath string) (string, error) {
	path, err := UnitPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create systemd user directory: %w", err)
	}
	unit, err := renderUnit(executable, configPath, ffprobePath)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("systemd service already exists at %s", path)
	}
	if err != nil {
		return "", fmt.Errorf("create systemd service: %w", err)
	}
	if _, err := file.WriteString(unit); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write systemd service: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close systemd service: %w", err)
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		removeUnit(ctx, path)
		return "", err
	}
	if err := systemctl(ctx, "enable", unitName); err != nil {
		removeUnit(ctx, path)
		return "", err
	}
	return path, nil
}

// Uninstall stops, disables, and removes the per-user Loom unit.
func Uninstall(ctx context.Context) error {
	path, err := UnitPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read systemd service: %w", err)
	}
	if !strings.HasPrefix(string(data), unitMarker+"\n") {
		return fmt.Errorf("refusing to remove systemd service not installed by Loom: %s", path)
	}
	if err := systemctl(ctx, "disable", "--now", unitName); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove systemd service: %w", err)
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return nil
}

func Start(ctx context.Context) error   { return systemctl(ctx, "start", unitName) }
func Stop(ctx context.Context) error    { return systemctl(ctx, "stop", unitName) }
func Restart(ctx context.Context) error { return systemctl(ctx, "restart", unitName) }

// LingerEnabled reports whether the current user's systemd manager starts at boot.
func LingerEnabled(ctx context.Context) (bool, error) {
	output, err := run(ctx, "loginctl", "show-user", strconv.Itoa(os.Getuid()), "--property=Linger", "--value")
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(output) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("loginctl returned an unexpected linger value %q", strings.TrimSpace(output))
	}
}

func renderUnit(executable, configPath, ffprobePath string) (string, error) {
	executable, err := absolutePath(executable, "Loom executable")
	if err != nil {
		return "", err
	}
	configPath, err = absolutePath(configPath, "config file")
	if err != nil {
		return "", err
	}
	ffprobePath, err = absolutePath(ffprobePath, "ffprobe executable")
	if err != nil {
		return "", err
	}
	path := strings.Join(uniquePaths(filepath.Dir(ffprobePath), "/usr/local/bin", "/usr/bin", "/bin"), ":")

	return fmt.Sprintf(`%s
[Unit]
Description=Loom media server

[Service]
Type=simple
ExecStart=%s --config %s daemon
Environment=%s
Restart=on-failure
RestartSec=10s
TimeoutStopSec=30s

[Install]
WantedBy=default.target
`, unitMarker, quoteExec(executable), quoteExec(configPath), quote("PATH="+path)), nil
}

func absolutePath(path, description string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s path is empty", description)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", description, err)
	}
	if strings.ContainsAny(absolute, "\x00\r\n") {
		return "", fmt.Errorf("%s path contains an unsupported character", description)
	}
	return filepath.Clean(absolute), nil
}

func quote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, `%`, `%%`)
	return `"` + value + `"`
}

func quoteExec(value string) string {
	return quote(strings.ReplaceAll(value, `$`, `$$`))
}

func uniquePaths(paths ...string) []string {
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	return result
}

func systemctl(ctx context.Context, args ...string) error {
	_, err := run(ctx, "systemctl", append([]string{"--user"}, args...)...)
	return err
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%s: %s", strings.Join(append([]string{name}, args...), " "), detail)
	}
	return string(output), nil
}

func removeUnit(ctx context.Context, path string) {
	_ = systemctl(ctx, "disable", unitName)
	_ = os.Remove(path)
	_ = systemctl(ctx, "daemon-reload")
}
