package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFFProberCommand(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ffprobe")
	prober := NewFFProber(path)
	if prober.Timeout != 2*time.Minute {
		t.Fatalf("timeout = %s", prober.Timeout)
	}
	for _, tc := range []struct {
		name, script, wantErr string
		duration              int64
	}{
		{"success", `printf '%s\n' '{"format":{"duration":"2.5","format_name":"matroska"},"streams":[{"codec_type":"video","codec_name":"hevc"}]}'`, "", 2500},
		{"invalid JSON", `echo 'oops'`, "decode ffprobe output", 0},
		{"stderr", `echo 'bad media' >&2; exit 1`, "bad media", 0},
		{"exit without stderr", `exit 2`, "exit status 2", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			result, err := prober.Probe(context.Background(), "movie.mkv")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Probe: %+v, %v", result, err)
				}
			} else if err != nil || result.DurationMS != tc.duration || result.Container != "matroska" || len(result.Streams) != 1 {
				t.Fatalf("Probe: %+v, %v", result, err)
			}
		})
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := prober.Probe(context.Background(), "missing.mkv"); err == nil || !strings.Contains(err.Error(), "ffprobe") {
		t.Fatalf("missing command: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prober.Probe(ctx, "movie.mkv"); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("canceled probe: %v", err)
	}
}
