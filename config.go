package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const appName = "omada-autologin"

// Config is the on-disk configuration. It is intentionally small: one
// account, one set of portal hosts, one polling interval.
type Config struct {
	Username string `json:"username"`
	Password string `json:"password"`

	// PortalURL is an optional last-known-good portal entry URL. It is used
	// when the gateway's interception page cannot be parsed.
	PortalURL string `json:"portal_url,omitempty"`

	// PortalHosts is an allow-list. Credentials are never sent anywhere else.
	PortalHosts []string `json:"portal_hosts,omitempty"`

	// CheckURLs are plain-HTTP URLs used to detect a captive portal.
	CheckURLs []string `json:"check_urls,omitempty"`

	// IntervalSeconds is the normal (online) health-check interval. The
	// daemon reacts immediately to link/address changes on Linux, so this
	// only needs to be a low-frequency safety net.
	IntervalSeconds int `json:"interval_seconds,omitempty"`

	// RetryMinSeconds/RetryMaxSeconds bound the exponential backoff used
	// while the network is offline or the portal rejects a login.
	RetryMinSeconds int `json:"retry_min_seconds,omitempty"`
	RetryMaxSeconds int `json:"retry_max_seconds,omitempty"`

	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

var (
	errNotConfigured = errors.New("not configured")
	// flagConfig is set by --config on any subcommand.
	flagConfig string
)

func defaultConfig() Config {
	return Config{
		PortalHosts: []string{"192.168.1.3"},
		CheckURLs: []string{
			"http://example.com/",
			"http://connectivitycheck.gstatic.com/generate_204",
			"http://captive.apple.com/hotspot-detect.html",
		},
		IntervalSeconds: 60,
		RetryMinSeconds: 5,
		RetryMaxSeconds: 60,
		TimeoutSeconds:  8,
	}
}

func (c *Config) applyDefaults() {
	d := defaultConfig()
	if len(c.PortalHosts) == 0 {
		c.PortalHosts = d.PortalHosts
	}
	if len(c.CheckURLs) == 0 {
		c.CheckURLs = d.CheckURLs
	}
	if c.IntervalSeconds <= 0 {
		c.IntervalSeconds = d.IntervalSeconds
	}
	if c.RetryMinSeconds <= 0 {
		c.RetryMinSeconds = d.RetryMinSeconds
	}
	if c.RetryMaxSeconds <= 0 {
		c.RetryMaxSeconds = d.RetryMaxSeconds
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = d.TimeoutSeconds
	}

	for i, h := range c.PortalHosts {
		h = strings.TrimSpace(h)
		h = strings.TrimPrefix(h, "https://")
		h = strings.TrimPrefix(h, "http://")
		if host, _, ok := strings.Cut(h, "/"); ok {
			h = host
		}
		c.PortalHosts[i] = strings.ToLower(h)
	}
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.Username) == "" {
		return errors.New("username is empty")
	}
	if c.Password == "" {
		return errors.New("password is empty")
	}
	if len(c.PortalHosts) == 0 {
		return errors.New("portal_hosts is empty")
	}
	if c.IntervalSeconds < 5 {
		return fmt.Errorf("interval_seconds must be at least 5 (got %d)", c.IntervalSeconds)
	}
	if c.RetryMinSeconds < 2 {
		return fmt.Errorf("retry_min_seconds must be at least 2 (got %d)", c.RetryMinSeconds)
	}
	if c.RetryMaxSeconds < c.RetryMinSeconds {
		return fmt.Errorf("retry_max_seconds (%d) must be >= retry_min_seconds (%d)", c.RetryMaxSeconds, c.RetryMinSeconds)
	}
	if c.RetryMaxSeconds > 3600 {
		return fmt.Errorf("retry_max_seconds must be at most 3600 (got %d)", c.RetryMaxSeconds)
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 {
		return fmt.Errorf("timeout_seconds must be between 1 and 120 (got %d)", c.TimeoutSeconds)
	}
	for _, u := range c.CheckURLs {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("check URL %q must start with http:// or https://", u)
		}
	}
	return nil
}

func (c *Config) interval() time.Duration {
	return time.Duration(c.IntervalSeconds) * time.Second
}

func (c *Config) retryMin() time.Duration {
	return time.Duration(c.RetryMinSeconds) * time.Second
}

func (c *Config) retryMax() time.Duration {
	return time.Duration(c.RetryMaxSeconds) * time.Second
}

func (c *Config) timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// configPath resolves the config location. An explicit --config always wins,
// otherwise the OS-appropriate per-user config directory is used:
//
//	macOS:   ~/Library/Application Support/omada-autologin/config.json
//	Linux:   ~/.config/omada-autologin/config.json
//	Windows: %AppData%\omada-autologin\config.json
func configPath() (string, error) {
	if flagConfig != "" {
		abs, err := filepath.Abs(flagConfig)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find config directory: %w", err)
	}
	return filepath.Join(dir, appName, "config.json"), nil
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: no config at %s (run `omada-autologin init`)", errNotConfigured, path)
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func saveConfig(path string, c *Config) error {
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := marshalIndent(c)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// os.WriteFile does not change the mode of an existing file.
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// marshalIndent is like json.MarshalIndent but keeps URLs readable instead of
// escaping & as \u0026.
func marshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// stateDir is the per-user cache directory for state and logs.
func stateDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find cache directory: %w", err)
	}
	return filepath.Join(dir, appName), nil
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Best effort; Windows ignores most POSIX modes.
	_ = os.Chmod(dir, 0o700)
	return nil
}
