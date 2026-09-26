package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/five82/loom/internal/store"
)

func TestCLIAdditionalDaemonResponses(t *testing.T) {
	dir, cfg := testCLI(t)
	testCLIDaemon(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_loom/status":
			_, _ = fmt.Fprint(w, `{"pid":20,"scan":{"running":true,"library":"tv"},"last_scans":[{"library":"tv","status":"completed"}]}`)
		case "/_loom/unmatched", "/_loom/metadata/search":
			_, _ = fmt.Fprint(w, `{"items":[]}`)
		case "/api/v1/scan":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{}`)
		case "/_loom/metadata/match":
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	for _, tc := range []struct {
		args          []string
		want, wantErr string
	}{
		{[]string{"status"}, "Scan: running (tv)", ""},
		{[]string{"unmatched"}, "No unmatched items", ""},
		{[]string{"search", "tv", "Unknown"}, "", ""},
		{[]string{"scan", "shorts"}, "", "daemon returned HTTP status 500"},
		{[]string{"match", "1", "2"}, "", "daemon returned HTTP status 409"},
	} {
		output, err := runCLI(t, cfg, tc.args...)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%v: %q, %v", tc.args, output, err)
			}
		} else if err != nil || !strings.Contains(output, tc.want) {
			t.Errorf("%v: %q, %v", tc.args, output, err)
		}
	}
}

func TestCLIAuditFindsIntegrityProblems(t *testing.T) {
	dir, cfg := testCLI(t)
	db, err := store.Open(filepath.Join(dir, "state", "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	libraryID, scanID, err := db.StartScan(ctx, "movies", filepath.Join(dir, "movies"))
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := db.UpsertItem(ctx, store.ItemInput{LibraryID: libraryID, ScanID: scanID, SourceKey: "missing", Kind: "movie", Title: "Missing media"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishScan(ctx, libraryID, scanID, 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"developer", "audit"}, {"developer", "audit", "--json"}} {
		output, err := runCLI(t, cfg, args...)
		if err == nil || !strings.Contains(err.Error(), "catalog integrity problems found") || !strings.Contains(output, "Missing media") {
			t.Errorf("item %s, %v: %q, %v", strconv.FormatInt(itemID, 10), args, output, err)
		}
	}
}
