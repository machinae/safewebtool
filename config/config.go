// Package config holds the global application settings, filled in by the
// mcp command from its flags and environment variables.
//
// This package must NOT import any other webtool packages. Any webtool package
// can import config, but not the reverse. This prevents circular imports.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// DefaultChromeUserDataDir returns the default Chrome user data directory for the current OS.
func DefaultChromeUserDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome"), nil
	case "linux":
		return filepath.Join(home, ".config", "google-chrome"), nil
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(localAppData, "Google", "Chrome", "User Data"), nil
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// Config holds the global application settings.
type Config struct {
	// ChromeDataDir is the Chrome user data directory.
	ChromeDataDir string
	// CDPUrl is a remote CDP WebSocket URL (e.g. Cloudflare Browser Rendering).
	// When set, local Chrome discovery is skipped.
	CDPUrl string
	// CDPToken is the Bearer token for authenticating to a remote CDP endpoint.
	CDPToken string
	// MaxOutput truncates page-sourced output to this many characters. 0 means no limit.
	MaxOutput int
	// Timeout bounds one browser command: queue wait plus execution.
	Timeout time.Duration
	// MaxWorkers caps how many tab groups in a call run
	// concurrently, and how many tabs may be attached at once (opening a
	// tab past the limit is refused). Keep modest; some sites flag wide
	// bursts.
	MaxWorkers int
	// DemoMode draws a visible, gliding mouse cursor on the page for demo
	// recordings. Off by default.
	DemoMode bool
}

// AppConfig is the global application configuration.
var AppConfig *Config

func init() {
	AppConfig = DefaultConfig()
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() *Config {
	chromeDataDir, _ := DefaultChromeUserDataDir()
	return &Config{
		ChromeDataDir: chromeDataDir,
		Timeout:       10 * time.Second,
		MaxWorkers:    5,
	}
}

// Validate checks that the configuration values are valid.
func (c *Config) Validate() error {
	if c.Timeout < 0 {
		return fmt.Errorf("timeout must be positive, got %s", c.Timeout)
	}
	if c.MaxOutput < 0 {
		return fmt.Errorf("max-output must be positive, got %d", c.MaxOutput)
	}
	if c.MaxWorkers < 0 {
		return fmt.Errorf("max-tabs must be positive, got %d", c.MaxWorkers)
	}
	return nil
}
