package daemonrun

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/five82/loom/internal/channels"
	"github.com/five82/loom/internal/store"
)

func TestUpdateChannelsLogsSuccessfulReconciliation(t *testing.T) {
	catalog, err := store.Open(filepath.Join(t.TempDir(), "loom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	updateChannels(context.Background(), channels.NewWith(catalog, nil, time.UTC), logger)
	for _, want := range []string{"channel schedule updated", "channels_created=0", "channels_removed=0", "programs_pruned=0", "programs_added=0"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in log %q", want, output.String())
		}
	}
}

func TestExpandTCPListenAddressIPv6Wildcard(t *testing.T) {
	addresses := []net.Addr{
		testAddress("fd00::2/64"), testAddress("::1/128"),
		testAddress("192.0.2.1/24"), testAddress("fd00::2/64"),
	}
	got := expandTCPListenAddress("[::]:8097", "[::]:8097", addresses)
	want := []string{"[::1]:8097", "[fd00::2]:8097"}
	if !slices.Equal(got, want) {
		t.Fatalf("expanded IPv6 addresses = %v, want %v", got, want)
	}
}
