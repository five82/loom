package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIServiceInstallPreconditions(t *testing.T) {
	for _, tc := range []struct{ name, config, ffprobe, want string }{
		{"missing config", "missing", "", "config"},
		{"no config file", "default", "", "requires a config file"},
		{"missing ffprobe", "valid", "", "ffprobe was not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, cfg := testCLI(t)
			path := fakeCLIService(t, "")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if tc.config == "missing" {
				cfg = filepath.Join(dir, "missing.toml")
			}
			if tc.config == "default" {
				cfg = ""
			}
			output, err := runCLI(t, cfg, "service", "install")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("output = %q; error = %v, want %q", output, err, tc.want)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("unit created despite failure: %v", err)
			}
		})
	}
}

func TestCLIServiceInstallStopsExistingDaemonBeforeStarting(t *testing.T) {
	dir, cfg := testCLI(t)
	unit := fakeCLIService(t, "")
	if err := os.Remove(unit); err != nil {
		t.Fatal(err)
	}
	ffprobe := filepath.Join(os.Getenv("PATH"), "ffprobe")
	if err := os.WriteFile(ffprobe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	testCLIDaemon(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_loom/stop" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
	}))
	_, err := runCLI(t, cfg, "service", "install")
	if err == nil || !strings.Contains(err.Error(), "stop existing daemon") {
		t.Fatalf("install error = %v", err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatalf("unit not cleaned up: %v", err)
	}
}
