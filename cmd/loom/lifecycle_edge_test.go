package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestCLIInstalledServiceWithRunningDaemon(t *testing.T) {
	dir, cfg := testCLI(t)
	fakeCLIService(t, "")
	testCLIDaemon(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"start"}, "Daemon already running"},
		{[]string{"restart"}, "Daemon restarted"},
		{[]string{"stop"}, "Daemon stopped"},
	} {
		output, err := runCLI(t, cfg, tc.args...)
		if err != nil || !strings.Contains(output, tc.want) {
			t.Fatalf("%v: output = %q, err = %v", tc.args, output, err)
		}
	}
}
