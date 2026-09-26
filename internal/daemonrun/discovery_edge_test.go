package daemonrun

import (
	"net"
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

func TestDiscoveryNormalizesHostAndScopedIPv6(t *testing.T) {
	service, err := discoveryService("Loom", "loom-box.local.", []string{
		"[fe80::1234%en0]:8097", "[::1]:8097", "[::]:8097",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.HostName != "loom-box.local." || service.Port != 8097 ||
		len(service.IPs) != 1 || !service.IPs[0].Equal(net.ParseIP("fe80::1234")) {
		t.Fatalf("service = %+v", service)
	}
	if _, err := discoveryService("", "loom-box", []string{"192.0.2.1:8097"}); err == nil || !strings.Contains(err.Error(), "missing service instance name") {
		t.Fatalf("missing instance = %v", err)
	}
	if _, _, err := startDiscovery("", []string{"192.0.2.1:8097"}); err == nil || !strings.Contains(err.Error(), "missing service instance name") {
		t.Fatalf("start with missing instance = %v", err)
	}
}
