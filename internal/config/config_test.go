package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yohanesgre/android-studio-llm-proxy/internal/config"
)

func TestMissingConfigFileCreatesDefault(t *testing.T) {
	// Set CONFIG_PATH to a non-existent file.
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "nonexistent", "config.json")
	os.Setenv("CONFIG_PATH", cfgPath)
	defer os.Unsetenv("CONFIG_PATH")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected no error for missing config, got: %v", err)
	}
	if !cfg.Created {
		t.Error("expected Created to be true")
	}
	if len(cfg.Models) == 0 {
		t.Error("expected default models to be loaded")
	}

	// Verify the file was written.
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("expected config file to be created: %v", err)
	}
}

func TestValidConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	content := `{
		"models": {
			"deepseek-v4-flash": {
				"thinking": {"type": "enabled"},
				"reasoning_effort": "high"
			},
			"deepseek-v4-pro": {
				"thinking": {"type": "disabled"}
			}
		}
	}`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	os.Setenv("CONFIG_PATH", cfgPath)
	defer os.Unsetenv("CONFIG_PATH")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(cfg.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(cfg.Models))
	}

	flash, ok := cfg.Models["deepseek-v4-flash"]
	if !ok {
		t.Fatal("expected deepseek-v4-flash in models")
	}
	if flash["reasoning_effort"] != "high" {
		t.Errorf("expected reasoning_effort=high, got %v", flash["reasoning_effort"])
	}

	pro, ok := cfg.Models["deepseek-v4-pro"]
	if !ok {
		t.Fatal("expected deepseek-v4-pro in models")
	}
	thinking, ok := pro["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking to be a map")
	}
	if thinking["type"] != "disabled" {
		t.Errorf("expected thinking.type=disabled, got %v", thinking["type"])
	}
}

func TestInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{invalid json`), 0644); err != nil {
		t.Fatal(err)
	}

	os.Setenv("CONFIG_PATH", cfgPath)
	defer os.Unsetenv("CONFIG_PATH")

	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestHomeDirExpansion(t *testing.T) {
	// Test that default path uses home dir.
	os.Unsetenv("CONFIG_PATH")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "android-studio-llm-proxy", "config.json")
	if cfg.Path != expected {
		t.Errorf("expected path %q, got %q", expected, cfg.Path)
	}
}

func TestDefaultConfigMatchesExample(t *testing.T) {
	examplePath := filepath.Join("..", "..", "config.example.json")
	example, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("failed to read config.example.json: %v", err)
	}

	defaultPath := filepath.Join("default.json")
	defaultData, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatalf("failed to read default.json: %v", err)
	}

	if string(example) != string(defaultData) {
		t.Errorf("internal/config/default.json is out of sync with config.example.json")
	}
}

func TestEmptyModelsField(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	content := `{"models": {}}`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	os.Setenv("CONFIG_PATH", cfgPath)
	defer os.Unsetenv("CONFIG_PATH")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(cfg.Models) != 0 {
		t.Errorf("expected empty models, got %d", len(cfg.Models))
	}
}
