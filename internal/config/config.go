package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config contains Loom's operator-controlled settings.
type Config struct {
	SourcePath string `toml:"-"`
	Name       string `toml:"name"`

	API     APIConfig     `toml:"api"`
	Paths   PathsConfig   `toml:"paths"`
	Library LibraryConfig `toml:"library"`
	Scanner ScannerConfig `toml:"scanner"`
	TMDB    TMDBConfig    `toml:"tmdb"`
}

type APIConfig struct {
	Bind string `toml:"bind"`
}

type PathsConfig struct {
	StateDir string `toml:"state_dir"`
}

type LibraryConfig struct {
	MoviesDir string `toml:"movies_dir"`
	ShortsDir string `toml:"shorts_dir"`
	TVDir     string `toml:"tv_dir"`
}

type ScannerConfig struct {
	Interval string `toml:"interval"`
}

type TMDBConfig struct {
	APIKey   string `toml:"api_key"`
	Language string `toml:"language"`
}

func defaultConfig() *Config {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/"
	}
	return &Config{
		Name: "Loom",
		API:  APIConfig{Bind: "0.0.0.0:8097"},
		Paths: PathsConfig{
			StateDir: filepath.Join(home, ".local", "state", "loom"),
		},
		Library: LibraryConfig{
			MoviesDir: "/media/daspool/media/content/movies",
			ShortsDir: "/media/daspool/media/content/shorts",
			TVDir:     "/media/daspool/media/content/tv",
		},
		Scanner: ScannerConfig{Interval: "24h"},
		TMDB:    TMDBConfig{Language: "en-US"},
	}
}

// Validate checks configuration without touching library contents.
func (c *Config) Validate() error {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if len([]byte(name)) > 63 {
		return fmt.Errorf("name must be at most 63 bytes")
	}
	if strings.TrimSpace(c.API.Bind) == "" {
		return fmt.Errorf("api.bind must not be empty")
	}
	if _, _, err := net.SplitHostPort(c.API.Bind); err != nil {
		return fmt.Errorf("api.bind %q: %w", c.API.Bind, err)
	}
	if c.Paths.StateDir == "" {
		return fmt.Errorf("paths.state_dir must not be empty")
	}
	if c.Library.MoviesDir == "" {
		return fmt.Errorf("library.movies_dir must not be empty")
	}
	if c.Library.ShortsDir == "" {
		return fmt.Errorf("library.shorts_dir must not be empty")
	}
	if c.Library.TVDir == "" {
		return fmt.Errorf("library.tv_dir must not be empty")
	}
	if c.Library.MoviesDir == c.Library.ShortsDir ||
		c.Library.MoviesDir == c.Library.TVDir ||
		c.Library.ShortsDir == c.Library.TVDir {
		return fmt.Errorf("movie, short film, and TV library directories must differ")
	}
	if _, err := c.ScanInterval(); err != nil {
		return err
	}
	if strings.TrimSpace(c.TMDB.Language) == "" {
		return fmt.Errorf("tmdb.language must not be empty")
	}
	return nil
}

// ScanInterval returns the scheduled scan interval. Zero disables scheduling.
func (c *Config) ScanInterval() (time.Duration, error) {
	value := strings.TrimSpace(c.Scanner.Interval)
	if value == "" || value == "0" {
		return 0, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("scanner.interval %q: %w", value, err)
	}
	if interval < time.Minute {
		return 0, fmt.Errorf("scanner.interval must be 0 or at least 1m")
	}
	return interval, nil
}

// EnsureStateDir creates Loom-owned state directories.
func (c *Config) EnsureStateDir() error {
	for _, dir := range []string{c.Paths.StateDir, c.ImageDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create state directory %q: %w", dir, err)
		}
	}
	return nil
}

// ResetState removes all Loom-owned state while preserving the loaded config file.
func (c *Config) ResetState() error {
	if c.SourcePath == "" || !pathContains(c.Paths.StateDir, c.SourcePath) {
		if err := os.RemoveAll(c.Paths.StateDir); err != nil {
			return fmt.Errorf("remove state directory %q: %w", c.Paths.StateDir, err)
		}
		return nil
	}
	return removeAllExcept(c.Paths.StateDir, c.SourcePath)
}

func removeAllExcept(dir, preservePath string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state directory %q: %w", dir, err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if path == preservePath {
			continue
		}
		if pathContains(path, preservePath) {
			if entry.IsDir() {
				if err := removeAllExcept(path, preservePath); err != nil {
					return err
				}
			}
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove state path %q: %w", path, err)
		}
	}
	return nil
}

func pathContains(dir, path string) bool {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func (c *Config) DBPath() string        { return filepath.Join(c.Paths.StateDir, "loom.db") }
func (c *Config) ImageDir() string      { return filepath.Join(c.Paths.StateDir, "images") }
func (c *Config) DaemonLogPath() string { return filepath.Join(c.Paths.StateDir, "daemon.log") }
func (c *Config) DaemonConsoleLogPath() string {
	return filepath.Join(c.Paths.StateDir, "daemon-console.log")
}
func (c *Config) SocketPath() string { return filepath.Join(runtimeDir(), "loom.sock") }
func (c *Config) LockPath() string   { return filepath.Join(runtimeDir(), "loom.lock") }

func runtimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return "/tmp"
}
