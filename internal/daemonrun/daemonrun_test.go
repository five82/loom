package daemonrun

import (
	"net"
	"slices"
	"testing"
)

type testAddress string

func (a testAddress) Network() string { return "test" }
func (a testAddress) String() string  { return string(a) }

func TestExpandTCPListenAddress(t *testing.T) {
	interfaceAddresses := []net.Addr{
		testAddress("192.168.1.20/24"),
		testAddress("127.0.0.1/8"),
		testAddress("::1/128"),
		testAddress("192.168.1.20/24"),
	}

	// Go may represent an IPv4 wildcard listener as a dual-stack IPv6 socket.
	// Use the configured bind to decide which interface addresses to report.
	got := expandTCPListenAddress("[::]:8097", "0.0.0.0:8097", interfaceAddresses)
	want := []string{"127.0.0.1:8097", "192.168.1.20:8097"}
	if !slices.Equal(got, want) {
		t.Fatalf("expanded addresses = %v, want %v", got, want)
	}
}

func TestExpandTCPListenAddressPreservesSpecificBind(t *testing.T) {
	got := expandTCPListenAddress("192.168.1.20:8097", "192.168.1.20:8097", nil)
	want := []string{"192.168.1.20:8097"}
	if !slices.Equal(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
}

func TestDiscoveryService(t *testing.T) {
	service, err := discoveryService("Loom Test", "loom-box", []string{
		"127.0.0.1:8097",
		"192.168.1.20:8097",
		"[fd00::20]:8097",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.Instance != "Loom Test" {
		t.Fatalf("instance = %q", service.Instance)
	}
	if service.Service != loomServiceType || service.Port != 8097 {
		t.Fatalf("service = %q port %d", service.Service, service.Port)
	}
	if service.HostName != "loom-box.local." {
		t.Fatalf("hostname = %q", service.HostName)
	}
	if got, want := service.IPs, []net.IP{net.ParseIP("192.168.1.20"), net.ParseIP("fd00::20")}; !slices.EqualFunc(got, want, net.IP.Equal) {
		t.Fatalf("IPs = %v, want %v", got, want)
	}
}

func TestDiscoveryRequiresLANAddress(t *testing.T) {
	if _, err := discoveryService("Loom", "loom-box", []string{"127.0.0.1:8097"}); err == nil {
		t.Fatal("discoveryService accepted only a loopback address")
	}
}
