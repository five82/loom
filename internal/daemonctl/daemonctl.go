package daemonctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

var ErrNotRunning = errors.New("daemon is not running")

func IsRunning(lockPath, socketPath string) bool {
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return false
	}
	if locked {
		_ = lock.Unlock()
		return false
	}
	connection, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

type StartOptions struct {
	LockPath   string
	SocketPath string
	LogPath    string
	ConfigPath string
}

func Start(opts StartOptions) error {
	if IsRunning(opts.LockPath, opts.SocketPath) {
		return fmt.Errorf("daemon is already running")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	args := []string{"daemon"}
	if opts.ConfigPath != "" {
		args = append(args, "--config", opts.ConfigPath)
	}
	if err := os.MkdirAll(filepath.Dir(opts.LogPath), 0o755); err != nil {
		return fmt.Errorf("create daemon log directory: %w", err)
	}
	logFile, err := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon console log: %w", err)
	}
	command := exec.Command(executable, args...)
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start daemon: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		exited <- command.Wait()
		_ = logFile.Close()
	}()
	for range 40 {
		time.Sleep(250 * time.Millisecond)
		select {
		case err := <-exited:
			if err != nil {
				return fmt.Errorf("daemon exited during startup: %w", err)
			}
			return fmt.Errorf("daemon exited during startup")
		default:
		}
		if IsRunning(opts.LockPath, opts.SocketPath) {
			return nil
		}
	}
	return fmt.Errorf("daemon did not become ready within 10 seconds")
}

// WaitUntilRunning waits for a daemon started by an external supervisor.
func WaitUntilRunning(ctx context.Context, lockPath, socketPath string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if IsRunning(lockPath, socketPath) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("daemon did not become ready within %s", timeout)
		case <-ticker.C:
		}
	}
}

func Stop(lockPath, socketPath string) error {
	if !IsRunning(lockPath, socketPath) {
		return ErrNotRunning
	}
	response, err := do(socketPath, http.MethodPost, "/_loom/stop", nil)
	if err != nil {
		return fmt.Errorf("request daemon stop: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("request daemon stop: unexpected HTTP status %s", response.Status)
	}
	for range 40 {
		time.Sleep(250 * time.Millisecond)
		if !IsRunning(lockPath, socketPath) {
			return nil
		}
	}
	return fmt.Errorf("daemon did not stop within 10 seconds")
}

func GetJSON(socketPath, path string, target any) error {
	response, err := do(socketPath, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(response.Body).Decode(&body) == nil && body.Error != "" {
			return errors.New(body.Error)
		}
		return fmt.Errorf("daemon returned %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode daemon response: %w", err)
	}
	return nil
}

func PostJSON(socketPath, path string, body, target any) (int, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode daemon request: %w", err)
		}
	}
	response, err := do(socketPath, http.MethodPost, path, data)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			return response.StatusCode, fmt.Errorf("decode daemon response: %w", err)
		}
	}
	return response.StatusCode, nil
}

func do(socketPath, method, path string, body []byte) (*http.Response, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return client.Do(request)
}
