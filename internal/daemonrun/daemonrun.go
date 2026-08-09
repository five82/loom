package daemonrun

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/five82/loom/internal/config"
	"github.com/five82/loom/internal/httpapi"
	"github.com/five82/loom/internal/library"
	"github.com/five82/loom/internal/metadata"
	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

// Run starts the foreground daemon and blocks until graceful shutdown.
func Run(ctx context.Context, cfg *config.Config) error {
	if err := cfg.EnsureStateDir(); err != nil {
		return err
	}
	lock := flock.New(cfg.LockPath())
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("acquire daemon lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("another Loom daemon is running")
	}
	defer func() { _ = lock.Unlock() }()

	logFile, err := os.OpenFile(cfg.DaemonLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if info, statErr := os.Stderr.Stat(); statErr == nil && info.Mode()&os.ModeCharDevice != 0 {
		logger = slog.New(newMultiHandler(
			slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
			slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}),
		))
	}

	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		return fmt.Errorf("required command ffprobe was not found in PATH")
	}
	catalog, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer func() { _ = catalog.Close() }()
	interval, _ := cfg.ScanInterval()
	var metadataService *metadata.Service
	var metadataMatcher library.MetadataMatcher
	if cfg.TMDB.APIKey != "" {
		metadataService = metadata.New(catalog, tmdb.New(cfg.TMDB.APIKey, cfg.TMDB.Language), cfg.ImageDir(), logger)
		metadataMatcher = metadataService
	} else {
		logger.Warn("TMDB metadata disabled", "reason", "tmdb.api_key is empty")
	}
	scanner := library.NewScanner(catalog, library.NewFFProber(ffprobePath), metadataMatcher,
		cfg.Library.MoviesDir, cfg.Library.ShortsDir, cfg.Library.TVDir, logger)
	scans := library.NewManager(scanner, interval, logger)
	shutdownRequest := make(chan struct{}, 1)

	_ = os.Remove(cfg.SocketPath())
	unixListener, err := net.Listen("unix", cfg.SocketPath())
	if err != nil {
		return fmt.Errorf("listen on daemon socket: %w", err)
	}
	if err := os.Chmod(cfg.SocketPath(), 0o600); err != nil {
		_ = unixListener.Close()
		return fmt.Errorf("secure daemon socket: %w", err)
	}
	defer func() { _ = os.Remove(cfg.SocketPath()) }()

	tcpListener, err := net.Listen("tcp", cfg.API.Bind)
	if err != nil {
		_ = unixListener.Close()
		return fmt.Errorf("listen on LAN API %s: %w", cfg.API.Bind, err)
	}
	api := httpapi.New(catalog, scans, metadataService, shutdownRequest,
		httpapi.ListenAddresses{
			API: tcpListenAddresses(tcpListener, cfg.API.Bind), Control: unixListener.Addr().String(),
		})

	localServer := &http.Server{
		Handler: api.LocalHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	publicServer := &http.Server{
		Handler: api.PublicHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		scans.Run(runCtx)
	}()

	serveErrors := make(chan error, 2)
	serve := func(server *http.Server, listener net.Listener) {
		err := server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			serveErrors <- err
		}
	}
	go serve(localServer, unixListener)
	go serve(publicServer, tcpListener)
	logger.Info("daemon started", "pid", os.Getpid(), "api_bind", cfg.API.Bind,
		"socket", cfg.SocketPath(), "scan_interval", interval.String())

	var runErr error
	select {
	case <-ctx.Done():
	case <-shutdownRequest:
		logger.Info("daemon stop requested")
	case runErr = <-serveErrors:
		runErr = fmt.Errorf("HTTP server failed: %w", runErr)
	}
	logger.Info("daemon stopping")
	cancel()
	workers.Wait()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := publicServer.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shut down LAN API: %w", err)
	}
	if err := localServer.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shut down local API: %w", err)
	}
	logger.Info("daemon stopped")
	return runErr
}

func tcpListenAddresses(listener net.Listener, configuredBind string) []string {
	bound := listener.Addr().String()
	interfaceAddresses, err := net.InterfaceAddrs()
	if err != nil {
		return []string{bound}
	}
	return expandTCPListenAddress(bound, configuredBind, interfaceAddresses)
}

func expandTCPListenAddress(bound, configuredBind string, interfaceAddresses []net.Addr) []string {
	configuredHost, _, err := net.SplitHostPort(configuredBind)
	if err != nil {
		return []string{bound}
	}
	configuredIP := net.ParseIP(configuredHost)
	if configuredIP == nil || !configuredIP.IsUnspecified() {
		return []string{bound}
	}
	_, port, err := net.SplitHostPort(bound)
	if err != nil {
		return []string{bound}
	}
	seen := make(map[string]bool)
	var addresses []string
	for _, interfaceAddress := range interfaceAddresses {
		ip, _, err := net.ParseCIDR(interfaceAddress.String())
		if err != nil || (configuredIP.To4() != nil) != (ip.To4() != nil) {
			continue
		}
		address := net.JoinHostPort(ip.String(), port)
		if !seen[address] {
			seen[address] = true
			addresses = append(addresses, address)
		}
	}
	if len(addresses) == 0 {
		return []string{bound}
	}
	sort.Strings(addresses)
	return addresses
}

type multiHandler struct {
	handlers []slog.Handler
}

func newMultiHandler(handlers ...slog.Handler) *multiHandler {
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, handler := range h.handlers {
		if err := handler.Handle(ctx, record.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return newMultiHandler(handlers...)
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return newMultiHandler(handlers...)
}
