package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/five82/loom/internal/config"
	"github.com/five82/loom/internal/daemonctl"
	"github.com/five82/loom/internal/daemonrun"
	"github.com/five82/loom/internal/store"
	"github.com/five82/loom/internal/tmdb"
)

var configPath string

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	root := newRootCommand()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "loom",
		Short:         "A direct-play movie and TV media server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&configPath, "config", "c", "", "Configuration file path")
	root.AddCommand(
		newStartCommand(), newStopCommand(), newRestartCommand(), newStatusCommand(),
		newScanCommand(), newUnmatchedCommand(), newSearchCommand(), newMatchCommand(),
		newLogsCommand(), newConfigCommand(), newDaemonCommand(),
	)
	return root
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func newStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the Loom daemon",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if daemonctl.IsRunning(cfg.LockPath(), cfg.SocketPath()) {
				fmt.Println("Daemon already running")
				return nil
			}
			if err := cfg.EnsureStateDir(); err != nil {
				return err
			}
			if err := daemonctl.Start(daemonctl.StartOptions{
				LockPath: cfg.LockPath(), SocketPath: cfg.SocketPath(),
				LogPath: cfg.DaemonConsoleLogPath(), ConfigPath: cfg.SourcePath,
			}); err != nil {
				return err
			}
			fmt.Println("Daemon started")
			return nil
		},
	}
}

func newStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Loom daemon",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			err = daemonctl.Stop(cfg.LockPath(), cfg.SocketPath())
			if errors.Is(err, daemonctl.ErrNotRunning) {
				fmt.Println("Daemon is not running")
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Println("Daemon stopped")
			return nil
		},
	}
}

func newRestartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the Loom daemon",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			err = daemonctl.Stop(cfg.LockPath(), cfg.SocketPath())
			if err != nil && !errors.Is(err, daemonctl.ErrNotRunning) {
				return err
			}
			if err := cfg.EnsureStateDir(); err != nil {
				return err
			}
			if err := daemonctl.Start(daemonctl.StartOptions{
				LockPath: cfg.LockPath(), SocketPath: cfg.SocketPath(),
				LogPath: cfg.DaemonConsoleLogPath(), ConfigPath: cfg.SourcePath,
			}); err != nil {
				return err
			}
			fmt.Println("Daemon restarted")
			return nil
		},
	}
}

type daemonStatus struct {
	Running bool `json:"running"`
	PID     int  `json:"pid"`
	Scan    struct {
		Running     bool   `json:"running"`
		Library     string `json:"library"`
		StartedAt   string `json:"started_at"`
		LastEndedAt string `json:"last_ended_at"`
		LastError   string `json:"last_error"`
	} `json:"scan"`
	Catalog struct {
		Movies    int `json:"movies"`
		Shows     int `json:"shows"`
		Episodes  int `json:"episodes"`
		Unmatched int `json:"unmatched"`
		Media     int `json:"media_files"`
	} `json:"catalog"`
}

func newStatusCommand() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Show daemon and catalog status",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !daemonctl.IsRunning(cfg.LockPath(), cfg.SocketPath()) {
				if asJSON {
					fmt.Println(`{"running":false}`)
				} else {
					fmt.Println("Daemon stopped")
				}
				return nil
			}
			var status daemonStatus
			if err := daemonctl.GetJSON(cfg.SocketPath(), "/_loom/status", &status); err != nil {
				return err
			}
			if asJSON {
				return printJSON(status)
			}
			fmt.Printf("Daemon running (PID %d)\n", status.PID)
			if status.Scan.Running {
				fmt.Printf("Scan: running (%s)\n", status.Scan.Library)
			} else if status.Scan.LastError != "" {
				fmt.Printf("Scan: idle; last scan failed: %s\n", status.Scan.LastError)
			} else {
				fmt.Println("Scan: idle")
			}
			fmt.Printf("Catalog: %d movies, %d shows, %d episodes, %d unmatched, %d media files\n",
				status.Catalog.Movies, status.Catalog.Shows, status.Catalog.Episodes,
				status.Catalog.Unmatched, status.Catalog.Media)
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return command
}

func newScanCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "scan [movies|tv]",
		Short: "Start a manual library scan",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			library := ""
			if len(args) == 1 {
				library = args[0]
			}
			if library != "" && library != "movies" && library != "tv" {
				return fmt.Errorf("library must be movies or tv")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !daemonctl.IsRunning(cfg.LockPath(), cfg.SocketPath()) {
				return daemonctl.ErrNotRunning
			}
			var response map[string]any
			status, err := daemonctl.PostJSON(cfg.SocketPath(), "/_loom/scan",
				map[string]string{"library": library}, &response)
			if err != nil {
				return err
			}
			if status == 409 {
				return fmt.Errorf("a library scan is already running")
			}
			if status != 202 {
				return fmt.Errorf("daemon returned HTTP status %d", status)
			}
			if library == "" {
				library = "all libraries"
			}
			fmt.Printf("Scan started: %s\n", library)
			return nil
		},
	}
}

func newUnmatchedCommand() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "unmatched",
		Short: "List items that need a metadata match",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !daemonctl.IsRunning(cfg.LockPath(), cfg.SocketPath()) {
				return daemonctl.ErrNotRunning
			}
			var response struct {
				Items []store.Item `json:"items"`
			}
			if err := daemonctl.GetJSON(cfg.SocketPath(), "/_loom/unmatched", &response); err != nil {
				return err
			}
			if asJSON {
				return printJSON(response.Items)
			}
			if len(response.Items) == 0 {
				fmt.Println("No unmatched items")
				return nil
			}
			for _, item := range response.Items {
				year := ""
				if item.Year > 0 {
					year = fmt.Sprintf(" (%d)", item.Year)
				}
				fmt.Printf("%d\t%s\t%s%s\n", item.ID, item.Kind, item.Title, year)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return command
}

func newSearchCommand() *cobra.Command {
	var year int
	var asJSON bool
	command := &cobra.Command{
		Use:   "search <movie|tv> <title>",
		Short: "Search TMDB for a manual metadata match",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			mediaType := args[0]
			if mediaType != "movie" && mediaType != "tv" {
				return fmt.Errorf("type must be movie or tv")
			}
			if year != 0 && (year < 1800 || year > 3000) {
				return fmt.Errorf("year must be between 1800 and 3000")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !daemonctl.IsRunning(cfg.LockPath(), cfg.SocketPath()) {
				return daemonctl.ErrNotRunning
			}
			query := url.Values{
				"type": {mediaType}, "query": {strings.Join(args[1:], " ")},
			}
			if year > 0 {
				query.Set("year", strconv.Itoa(year))
			}
			var response struct {
				Items []tmdb.SearchResult `json:"items"`
			}
			if err := daemonctl.GetJSON(cfg.SocketPath(), "/_loom/metadata/search?"+query.Encode(), &response); err != nil {
				return err
			}
			if asJSON {
				return printJSON(response.Items)
			}
			for _, result := range response.Items {
				yearText := ""
				if result.Year > 0 {
					yearText = fmt.Sprintf(" (%d)", result.Year)
				}
				fmt.Printf("%d\t%s%s\n", result.ID, result.Title, yearText)
			}
			return nil
		},
	}
	command.Flags().IntVar(&year, "year", 0, "Release or first-air year")
	command.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return command
}

func newMatchCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "match <item-id> <tmdb-id>",
		Short: "Apply a TMDB match to a movie or show",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			itemID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || itemID <= 0 {
				return fmt.Errorf("item-id must be a positive integer")
			}
			tmdbID, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil || tmdbID <= 0 {
				return fmt.Errorf("tmdb-id must be a positive integer")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !daemonctl.IsRunning(cfg.LockPath(), cfg.SocketPath()) {
				return daemonctl.ErrNotRunning
			}
			var response map[string]string
			status, err := daemonctl.PostJSON(cfg.SocketPath(), "/_loom/metadata/match",
				map[string]int64{"item_id": itemID, "tmdb_id": tmdbID}, &response)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				if message := response["error"]; message != "" {
					return errors.New(message)
				}
				return fmt.Errorf("daemon returned HTTP status %d", status)
			}
			fmt.Printf("Matched item %d to TMDB %d\n", itemID, tmdbID)
			return nil
		},
	}
}

func newConfigCommand() *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Initialize or validate configuration"}
	command.AddCommand(
		&cobra.Command{
			Use:   "init",
			Short: "Write a starter config.toml",
			RunE: func(_ *cobra.Command, _ []string) error {
				path, err := config.WriteSample(configPath)
				if err != nil {
					return err
				}
				fmt.Println(path)
				return nil
			},
		},
		&cobra.Command{
			Use:   "validate",
			Short: "Load and validate config.toml",
			RunE: func(_ *cobra.Command, _ []string) error {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if cfg.SourcePath == "" {
					fmt.Println("Configuration valid (built-in defaults)")
				} else {
					fmt.Printf("Configuration valid: %s\n", cfg.SourcePath)
				}
				return nil
			},
		},
	)
	return command
}

func newDaemonCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Run the Loom daemon in the foreground",
		Hidden: true,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return daemonrun.Run(command.Context(), cfg)
		},
	}
}

func newLogsCommand() *cobra.Command {
	var follow bool
	var lines int
	command := &cobra.Command{
		Use:   "logs",
		Short: "Display daemon logs",
		RunE: func(command *cobra.Command, _ []string) error {
			if lines < 0 {
				return fmt.Errorf("lines must be non-negative")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return displayLogs(command.Context(), cfg.DaemonLogPath(), lines, follow)
		},
	}
	command.Flags().BoolVarP(&follow, "follow", "f", false, "Follow new log records")
	command.Flags().IntVarP(&lines, "lines", "n", 100, "Number of existing records to show")
	return command
}

func displayLogs(ctx context.Context, path string, lineCount int, follow bool) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() { _ = file.Close() }()
	var recent []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if lineCount == 0 {
			continue
		}
		if len(recent) == lineCount {
			copy(recent, recent[1:])
			recent[len(recent)-1] = scanner.Text()
		} else {
			recent = append(recent, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read daemon log: %w", err)
	}
	for _, line := range recent {
		fmt.Println(line)
	}
	if !follow {
		return nil
	}
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			fmt.Print(line)
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func printJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
