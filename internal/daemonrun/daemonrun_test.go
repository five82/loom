package daemonrun

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/five82/loom/internal/store"
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

func TestMultiHandlerFansOutAndPreservesAttributes(t *testing.T) {
	var left, right bytes.Buffer
	ctx := context.Background()
	h := newMultiHandler(
		slog.NewTextHandler(&left, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewTextHandler(&right, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)
	if h.Enabled(ctx, slog.LevelDebug) || !h.Enabled(ctx, slog.LevelInfo) {
		t.Fatal("unexpected combined log levels")
	}
	logger := slog.New(h.WithAttrs([]slog.Attr{slog.String("component", "daemon")}).WithGroup("status"))
	logger.InfoContext(ctx, "started", "port", 8097)
	if !strings.Contains(left.String(), "component=daemon") || !strings.Contains(left.String(), "status.port=8097") || !strings.Contains(right.String(), "status.port=8097") {
		t.Fatalf("info outputs: left=%q right=%q", left.String(), right.String())
	}
	logger.WarnContext(ctx, "stopping")
	if !strings.Contains(left.String(), "stopping") || !strings.Contains(right.String(), "stopping") {
		t.Fatalf("warn outputs: left=%q right=%q", left.String(), right.String())
	}
}

// A handler error must be returned to slog rather than silently swallowed.
type failingHandler struct{ slog.Handler }

func (f failingHandler) Handle(context.Context, slog.Record) error { return errors.New("write failed") }

func TestMultiHandlerReturnsFirstError(t *testing.T) {
	var output bytes.Buffer
	h := newMultiHandler(failingHandler{slog.NewTextHandler(&output, nil)}, slog.NewTextHandler(&output, nil))
	if err := h.Handle(context.Background(), slog.Record{}); err == nil || err.Error() != "write failed" {
		t.Fatalf("Handle error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("second handler ran: %q", output.String())
	}
}

func TestTCPListenAddressesUsesBoundPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	got := tcpListenAddresses(listener, "127.0.0.1:0")
	if !slices.Equal(got, []string{listener.Addr().String()}) {
		t.Fatalf("addresses = %v", got)
	}
	for _, tc := range []struct {
		bound, bind string
		addresses   []net.Addr
	}{
		{"127.0.0.1:8097", "bad bind", nil},
		{"bad bound", "0.0.0.0:8097", nil},
		{"0.0.0.0:8097", "0.0.0.0:8097", []net.Addr{testAddress("bad cidr"), testAddress("::1/128")}},
	} {
		if got := expandTCPListenAddress(tc.bound, tc.bind, tc.addresses); !slices.Equal(got, []string{tc.bound}) {
			t.Fatalf("expand %q, %q = %v", tc.bound, tc.bind, got)
		}
	}
}

func TestCanceledBackgroundWork(t *testing.T) {
	catalog, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	runFeaturedPicks(ctx, catalog, logger)
	updateChannels(ctx, nil, logger) // Cancellation must avoid touching the lineup.
}

func TestDiscoveryRequiresLANAddress(t *testing.T) {
	if _, err := discoveryService("Loom", "loom-box", []string{"127.0.0.1:8097"}); err == nil {
		t.Fatal("discoveryService accepted only a loopback address")
	}
}
