package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestCLIServiceInstallStartsDaemonAndReportsLinger(t *testing.T) {
	dir, cfg := testCLI(t)
	unit := fakeCLIService(t, "")
	if err := os.Remove(unit); err != nil {
		t.Fatal(err)
	}
	bin := os.Getenv("PATH")
	if err := os.WriteFile(filepath.Join(bin, "ffprobe"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "started")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in *\"start loom.service\"*) /usr/bin/touch %q;; esac\n", marker)
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "loom.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	lock := flock.New(filepath.Join(dir, "loom.lock"))
	acquired := make(chan error, 1)
	go func() {
		deadline := time.After(5 * time.Second)
		for {
			if _, err := os.Stat(marker); err == nil {
				acquired <- lock.Lock()
				return
			}
			select {
			case <-deadline:
				acquired <- fmt.Errorf("systemctl did not start daemon")
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	output, err := runCLI(t, cfg, "service", "install")
	if err != nil || !strings.Contains(output, "Daemon started") {
		t.Fatalf("install: output %q, err %v", output, err)
	}
	if err := <-acquired; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("service unit missing: %v", err)
	}
}
