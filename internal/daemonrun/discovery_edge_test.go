package daemonrun

import (
	"strings"
	"testing"
)

func TestDiscoveryIgnoresBadBindingsAndRejectsPortConflicts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []string
		want      string
	}{
		{"malformed", []string{"not-a-bind"}, "no discoverable address"},
		{"bad port", []string{"192.0.2.1:invalid"}, "no discoverable address"},
		{"no LAN", []string{"0.0.0.0:8097", "127.0.0.1:8097"}, "no discoverable address"},
		{"two ports", []string{"192.0.2.1:8097", "192.0.2.2:8098"}, "multiple ports"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := discoveryService("Loom", "loom-host", tc.addresses); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("discovery = %v", err)
			}
		})
	}
	if _, _, err := startDiscovery("Loom", []string{"invalid"}); err == nil || !strings.Contains(err.Error(), "no discoverable address") {
		t.Fatalf("start discovery = %v", err)
	}
}
