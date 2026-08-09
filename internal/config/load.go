package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Load reads configuration from an explicit path, the XDG configuration
// directory, ./loom.toml, or built-in defaults, in that order.
func Load(explicitPath string) (*Config, error) {
	cfg := defaultConfig()
	data, sourcePath, err := findConfig(explicitPath)
	if err != nil {
		return nil, err
	}
	if data != nil {
		decoder := toml.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(cfg); err != nil {
			return nil, fmt.Errorf("parse config %q: %w", sourcePath, err)
		}
	}
	cfg.SourcePath = sourcePath
	cfg.Name = strings.TrimSpace(cfg.Name)

	if apiKey := os.Getenv("TMDB_API_KEY"); apiKey != "" {
		cfg.TMDB.APIKey = apiKey
	}
	if err := normalizePaths(cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

func findConfig(explicitPath string) ([]byte, string, error) {
	if explicitPath != "" {
		path, err := absolutePath(explicitPath)
		if err != nil {
			return nil, "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read config %q: %w", path, err)
		}
		return data, path, nil
	}

	var candidates []string
	if dir, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "loom", "config.toml"))
	}
	candidates = append(candidates, "loom.toml")
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err == nil {
			path, absErr := filepath.Abs(candidate)
			if absErr != nil {
				path = candidate
			}
			return data, path, nil
		}
		if !os.IsNotExist(err) {
			return nil, "", fmt.Errorf("read config %q: %w", candidate, err)
		}
	}
	return nil, "", nil
}

func normalizePaths(cfg *Config) error {
	for _, value := range []*string{
		&cfg.Paths.StateDir,
		&cfg.Library.MoviesDir,
		&cfg.Library.ShortsDir,
		&cfg.Library.TVDir,
	} {
		path, err := absolutePath(*value)
		if err != nil {
			return err
		}
		*value = path
	}
	return nil
}

func absolutePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	return filepath.Clean(absolute), nil
}

// DefaultPath returns the XDG-aware default configuration path.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(dir, "loom", "config.toml"), nil
}

// WriteSample writes a minimal configuration without replacing an existing file.
func WriteSample(path string) (string, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return "", err
		}
	} else {
		var err error
		path, err = absolutePath(path)
		if err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create config %q: %w", path, err)
	}
	if _, err := file.WriteString(sampleConfig); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write config %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close config %q: %w", path, err)
	}
	return path, nil
}

const sampleConfig = `name = "Loom"

[api]
bind = "0.0.0.0:8097"

[paths]
state_dir = "~/.local/state/loom"

[library]
movies_dir = "/media/daspool/media/content/movies"
shorts_dir = "/media/daspool/media/content/shorts"
tv_dir = "/media/daspool/media/content/tv"

[scanner]
# Set to "0" to disable scheduled scans. Scans never run automatically at startup.
interval = "24h"

[tmdb]
api_key = ""
language = "en-US"
`
