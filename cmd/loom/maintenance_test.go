package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestCLIMaintenance(t *testing.T) {
	dir, configFile := testCLI(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config-home"))
	dbPath := filepath.Join(dir, "state", "loom.db")
	backupPath := filepath.Join(dir, "snapshot.db")
	for _, tc := range []struct {
		args          []string
		want, wantErr string
	}{
		{[]string{"developer", "audit"}, "", "open catalog for audit"},
		{[]string{"backup", backupPath}, "", "open"},
		{[]string{"migrate"}, "Created catalog schema version", ""},
		{[]string{"migrate"}, "Catalog schema already at version", ""},
		{[]string{"developer", "audit"}, "0 integrity problems", ""},
		{[]string{"developer", "audit", "--json"}, `"schema_version":`, ""},
		{[]string{"backup", backupPath}, backupPath, ""},
		{[]string{"backup", backupPath}, "", "exists"},
		{[]string{"backup"}, filepath.Join(dir, "state", "backups", "loom-"), ""},
		{[]string{"service", "uninstall"}, "systemd service is not installed", ""},
		{[]string{"stop"}, "Daemon is not running", ""},
		{[]string{"logs"}, "", "open daemon log"},
	} {
		output, err := runCLI(t, configFile, tc.args...)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%v: output %q, error %v; want %q", tc.args, output, err, tc.wantErr)
			}
		} else if err != nil || !strings.Contains(output, tc.want) {
			t.Errorf("%v: output %q, error %v; want %q", tc.args, output, err, tc.want)
		}
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, configFile, "developer", "reset")
	if err != nil || !strings.Contains(output, "Loom reset") {
		t.Fatalf("reset: %q %v", output, err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("database after reset: %v", err)
	}
	if _, err := os.Stat(configFile); err != nil {
		t.Fatalf("config after reset: %v", err)
	}
}

func TestCLIServiceRequiresConfig(t *testing.T) {
	dir, _ := testCLI(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config-home"))
	t.Setenv("HOME", dir)
	output, err := runCLI(t, "", "service", "install")
	if err == nil || !strings.Contains(err.Error(), "requires a config file") {
		t.Fatalf("install: %q %v", output, err)
	}
}

func TestCLIMigrateAndStartRefuseRunningDaemon(t *testing.T) {
	dir, configFile := testCLI(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config-home"))
	testCLIDaemon(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"migrate"}, "stop Loom before migrating"},
		{[]string{"start"}, "Daemon already running"},
	} {
		output, err := runCLI(t, configFile, tc.args...)
		if tc.want == "Daemon already running" {
			if err != nil || !strings.Contains(output, tc.want) {
				t.Errorf("%v: %q %v", tc.args, output, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %q %v", tc.args, output, err)
		}
	}
}

func TestPrintAuditReportSections(t *testing.T) {
	report := store.AuditReport{SchemaVersion: 16, PlaybackStateRows: 2, ManualArtworkSelections: 1, Findings: []store.Finding{
		{Check: "broken relation", Integrity: true, Count: 1, Matches: []string{"item 7"}},
		{Check: "missing poster", Count: 2, Matches: []string{"Movie"}},
	}}
	// printAuditReport writes to stdout, like the CLI commands.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	printAuditReport("catalog.db", report)
	_ = w.Close()
	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	for _, want := range []string{"catalog.db", "2 playback rows", "Integrity", "item 7", "Metadata", "Movie", "1 integrity problems"} {
		if !strings.Contains(string(buf[:n]), want) {
			t.Errorf("report missing %q: %s", want, buf[:n])
		}
	}
}
