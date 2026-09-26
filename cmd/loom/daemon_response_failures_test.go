package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestCLIRejectsMalformedDaemonResponses(t *testing.T) {
	dir, cfg := testCLI(t)
	testCLIDaemon(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not JSON"))
	}))
	for _, args := range [][]string{
		{"status"}, {"status", "--json"}, {"unmatched"}, {"search", "movie", "Film"},
		{"scan"}, {"match", "1", "2"},
	} {
		output, err := runCLI(t, cfg, args...)
		if err == nil || !strings.Contains(err.Error(), "decode daemon response") {
			t.Fatalf("%v: output = %q, error = %v", args, output, err)
		}
	}
}
