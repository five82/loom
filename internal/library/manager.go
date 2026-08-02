package library

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Manager serializes manual and scheduled scans.
type Manager struct {
	scanner  *Scanner
	interval time.Duration
	logger   *slog.Logger
	requests chan string
	busy     atomic.Bool

	mu          sync.RWMutex
	library     string
	startedAt   string
	lastEndedAt string
	lastError   string
}

type ScanStatus struct {
	Running     bool   `json:"running"`
	Library     string `json:"library,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	LastEndedAt string `json:"last_ended_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

func NewManager(scanner *Scanner, interval time.Duration, logger *slog.Logger) *Manager {
	return &Manager{
		scanner: scanner, interval: interval, logger: logger,
		requests: make(chan string, 1),
	}
}

// Trigger queues a scan and reports false if another scan is running or queued.
func (m *Manager) Trigger(library string) bool {
	if !m.busy.CompareAndSwap(false, true) {
		return false
	}
	m.requests <- library
	return true
}

func (m *Manager) Status() ScanStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return ScanStatus{
		Running: m.busy.Load(), Library: m.library, StartedAt: m.startedAt,
		LastEndedAt: m.lastEndedAt, LastError: m.lastError,
	}
}

// Run blocks until the daemon context is canceled.
func (m *Manager) Run(ctx context.Context) {
	var ticker *time.Ticker
	var ticks <-chan time.Time
	if m.interval > 0 {
		ticker = time.NewTicker(m.interval)
		ticks = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case library := <-m.requests:
			m.runScan(ctx, library)
		case <-ticks:
			if m.busy.CompareAndSwap(false, true) {
				m.runScan(ctx, "")
			} else {
				m.logger.Info("scheduled library scan skipped", "reason", "scan already running")
			}
		}
	}
}

func (m *Manager) runScan(ctx context.Context, library string) {
	m.mu.Lock()
	m.library = library
	if m.library == "" {
		m.library = "all"
	}
	m.startedAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.lastError = ""
	m.mu.Unlock()

	err := m.scanner.Scan(ctx, library)

	m.mu.Lock()
	m.lastEndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		m.lastError = err.Error()
		m.logger.Error("library scan failed", "library", m.library, "error", err)
	}
	m.library = ""
	m.startedAt = ""
	m.mu.Unlock()
	m.busy.Store(false)
}
