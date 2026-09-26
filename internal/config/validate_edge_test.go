package config

import (
	"strings"
	"testing"
)

func TestValidateRejectsMissingFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{"name", func(c *Config) { c.Name = " " }, "name must not be empty"},
		{"long name", func(c *Config) { c.Name = strings.Repeat("x", 64) }, "at most 63 bytes"},
		{"empty bind", func(c *Config) { c.API.Bind = " " }, "api.bind must not be empty"},
		{"invalid bind", func(c *Config) { c.API.Bind = "not-a-host-port" }, "api.bind"},
		{"state", func(c *Config) { c.Paths.StateDir = "" }, "paths.state_dir"},
		{"movies", func(c *Config) { c.Library.MoviesDir = "" }, "library.movies_dir"},
		{"shorts", func(c *Config) { c.Library.ShortsDir = "" }, "library.shorts_dir"},
		{"tv", func(c *Config) { c.Library.TVDir = "" }, "library.tv_dir"},
		{"language", func(c *Config) { c.TMDB.Language = " " }, "tmdb.language"},
		{"bad interval", func(c *Config) { c.Scanner.Interval = "tomorrow" }, "scanner.interval"},
		{"short interval", func(c *Config) { c.Scanner.Interval = "59s" }, "at least 1m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultConfig()
			tc.change(c)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate: %v; want %q", err, tc.want)
			}
		})
	}
}
