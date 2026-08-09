package daemonrun

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/hashicorp/mdns"
)

const loomServiceType = "_loom._tcp"

func startDiscovery(instanceName string, addresses []string) (*mdns.Server, string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, "", fmt.Errorf("read hostname: %w", err)
	}
	service, err := discoveryService(instanceName, hostname, addresses)
	if err != nil {
		return nil, "", err
	}
	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return nil, "", err
	}
	return server, service.Instance, nil
}

func discoveryService(instanceName, hostname string, addresses []string) (*mdns.MDNSService, error) {
	var port int
	var ips []net.IP
	for _, address := range addresses {
		host, portText, err := net.SplitHostPort(address)
		if err != nil {
			continue
		}
		candidatePort, err := strconv.Atoi(portText)
		if err != nil {
			continue
		}
		if port == 0 {
			port = candidatePort
		} else if port != candidatePort {
			return nil, fmt.Errorf("LAN API has multiple ports")
		}
		// A scoped IPv6 address cannot be represented in an mDNS AAAA record.
		host, _, _ = strings.Cut(host, "%")
		ip := net.ParseIP(host)
		if ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
			ips = append(ips, ip)
		}
	}
	if port == 0 || len(ips) == 0 {
		return nil, fmt.Errorf("LAN API has no discoverable address")
	}

	hostname = strings.TrimSuffix(hostname, ".")
	dnsHost := strings.TrimSuffix(hostname, ".local") + ".local."
	return mdns.NewMDNSService(
		instanceName,
		loomServiceType,
		"local.",
		dnsHost,
		port,
		ips,
		[]string{"path=/api/v1"},
	)
}
