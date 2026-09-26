package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayLogsRejectsLongLinesAndEndsFollowOnCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 70<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := displayLogs(context.Background(), path, 10, false); err == nil || !strings.Contains(err.Error(), "read daemon log") {
		t.Fatalf("long line = %v", err)
	}
	if err := os.WriteFile(path, []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := displayLogs(ctx, path, 0, true); err != nil {
		t.Fatalf("canceled log follow = %v", err)
	}
}

func TestPrintJSONRejectsUnsupportedValue(t *testing.T) {
	if err := printJSON(make(chan int)); err == nil {
		t.Fatal("channel marshaled to JSON")
	}
}
