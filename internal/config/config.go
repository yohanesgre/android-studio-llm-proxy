// Package config provides configuration loading for the proxy.
//
// Configuration is loaded from a JSON file (default: ~/.config/android-studio-llm-proxy/config.json)
// and can be overridden by environment variables. The config file supports per-model
// overrides for settings like thinking mode and reasoning effort.
package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// defaultConfigJSON is embedded at build time from default.json.
// Keep default.json in sync with the root config.example.json.
//
//go:embed default.json
var defaultConfigJSON []byte

// ModelOverrides maps model IDs to their override settings.
type ModelOverrides map[string]map[string]any

// Config holds the proxy configuration.
type Config struct {
	Path            string
	Created         bool
	UpstreamURL     string
	Port            string
	CacheTTL        time.Duration
	CacheMaxEntries int
	Models          ModelOverrides
}

// Load reads configuration from the config file and environment variables.
// If the config file does not exist, it creates the directory and writes a
// default config file, then loads it.
func Load() (*Config, error) {
	cfg := &Config{
		Models: make(ModelOverrides),
	}

	// Resolve config path.
	cfgPath := os.Getenv("CONFIG_PATH")
	if cfgPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("config: get home dir: %w", err)
		}
		cfgPath = filepath.Join(home, ".config", "android-studio-llm-proxy", "config.json")
	}
	cfg.Path = cfgPath

	// Load config file if it exists.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create a default config file on first run.
			if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
				return nil, fmt.Errorf("config: create config dir: %w", err)
			}
			if err := os.WriteFile(cfgPath, defaultConfigJSON, 0644); err != nil {
				return nil, fmt.Errorf("config: write default config: %w", err)
			}
			cfg.Created = true
			data = defaultConfigJSON
		} else {
			return nil, fmt.Errorf("config: read file: %w", err)
		}
	}

	var raw struct {
		Models map[string]map[string]any `json:"models"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: invalid JSON: %w", err)
	}

	if raw.Models != nil {
		cfg.Models = raw.Models
	}

	return cfg, nil
}
