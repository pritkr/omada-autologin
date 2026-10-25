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

const (
	appName = "omada-autologin"

	// New configs rotate the last three digits of the username through this
	// range by default. Set username_rotation.enabled to false to keep one
	// fixed username.
	defaultSuffixDigits = 3
	defaultSuffixMax    = 112

	// Login retries are deliberately small: enough to survive one bad suffix
	// or a portal timeout, without hammering the controller.
	defaultLoginAttempts = 3
	defaultLoginSettle   = 8
)

// Config is the on-disk configuration. It is intentionally small: one
// credential set (optionally rotating a numeric username suffix), one set of
// portal hosts, one polling interval.
type Config struct {
	Username string `json:"username"`
	Password string `json:"password"`

	// UsernameRotation is optional. When enabled, the last Digits characters
	// of Username (which must be digits) are replaced with a random number
	// from [Min, Max] on every login attempt. The password is unchanged.
	UsernameRotation *UsernameRotation `json:"username_rotation,omitempty"`

	// WiFi reconnects to a known/saved Wi-Fi network when the link drops or
	// the portal becomes unreachable. It never joins an arbitrary network:
	// targets come from SSIDs below and the last SSID that successfully
	// served the portal.
	WiFi *WiFiConfig `json:"wifi,omitempty"`

	// LoginAttempts is how many usernames to try in one check before backing
	// off. Defaults to 3.
	LoginAttempts int `json:"login_attempts,omitempty"`

	// LoginSettleSeconds is how long to wait for internet after the portal
	// accepts a login. Defaults to 8.
	LoginSettleSeconds int `json:"login_settle_seconds,omitempty"`

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

// UsernameRotation replaces the trailing digits of a username on each login.
// New configs enable it by default; it is always bounded to the numeric range
// declared in the config and never enumerates or guesses usernames outside it.
type UsernameRotation struct {
	Enabled bool `json:"enabled"`
	// Digits is how many trailing characters of Username to replace.
	// Defaults to 3.
	Digits int `json:"digits,omitempty"`
	// Min and Max bound the replacement number, inclusive.
	Min int `json:"min,omitempty"`
	Max int `json:"max,omitempty"`
	// SuffixOnly intentionally sends just the generated number, with no
	// username prefix. It is only valid when username is empty.
	SuffixOnly bool `json:"suffix_only,omitempty"`
}

// WiFiConfig controls automatic Wi-Fi recovery. Passwords are never stored
// here: networks must already be saved in the OS keychain / connection
// manager, so the daemon can reconnect without handling a secret.
type WiFiConfig struct {
	Enabled bool `json:"enabled"`
	// Interface overrides auto-detection (for example "en0" on macOS).
	Interface string `json:"interface,omitempty"`
	// SSIDs are tried in order when the link is down. The last SSID that
	// successfully served the portal is also remembered in state.json.
	SSIDs []string `json:"ssids,omitempty"`
	// Prefer attempts a configured SSID even when a different network
	// currently has internet. Off by default so a working hotspot is not
	// interrupted.
	Prefer bool `json:"prefer,omitempty"`
	// ResetOnStall power-cycles the interface after repeated failures.
	ResetOnStall bool `json:"reset_on_stall,omitempty"`
}

var (
	errNotConfigured = errors.New("not configured")
	// flagConfig is set by --config on any subcommand.
	flagConfig string
)

func defaultConfig() Config {
	return Config{
		UsernameRotation: &UsernameRotation{
			Enabled: true,
			Digits:  defaultSuffixDigits,
			Min:     1,
			Max:     defaultSuffixMax,
		},
		// New configs learn the portal SSID after the first successful login
		// and can reconnect to it automatically. No secret is stored.
		WiFi:        &WiFiConfig{Enabled: true},
		PortalHosts: []string{"192.168.1.3"},
		CheckURLs: []string{
			"http://example.com/",
			"http://connectivitycheck.gstatic.com/generate_204",
			"http://captive.apple.com/hotspot-detect.html",
		},
		IntervalSeconds:    60,
		RetryMinSeconds:    5,
		RetryMaxSeconds:    60,
		TimeoutSeconds:     8,
		LoginAttempts:      defaultLoginAttempts,
		LoginSettleSeconds: defaultLoginSettle,
	}
}

func (c *Config) applyDefaults() {
	d := defaultConfig()
	c.Username = strings.TrimSpace(c.Username)
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
	// Zero means "unset" (JSON omits it); negative values are configuration
	// errors and must not be silently replaced by a default.
	if c.LoginAttempts == 0 {
		c.LoginAttempts = d.LoginAttempts
	}
	if c.LoginSettleSeconds == 0 {
		c.LoginSettleSeconds = d.LoginSettleSeconds
	}
	c.UsernameRotation.applyDefaults()
	c.WiFi.applyDefaults()

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

func (r *UsernameRotation) applyDefaults() {
	if r == nil || !r.Enabled {
		return
	}
	if r.Digits == 0 {
		r.Digits = defaultSuffixDigits
	}
	if r.Min == 0 {
		r.Min = 1
	}
	if r.Max == 0 {
		r.Max = defaultSuffixMaxFor(r.Digits)
	}
}

func (w *WiFiConfig) applyDefaults() {
	if w == nil {
		return
	}
	w.Interface = strings.TrimSpace(w.Interface)
	seen := make(map[string]bool, len(w.SSIDs))
	out := w.SSIDs[:0]
	for _, ssid := range w.SSIDs {
		ssid = strings.TrimSpace(ssid)
		if ssid == "" || seen[ssid] {
			continue
		}
		seen[ssid] = true
		out = append(out, ssid)
	}
	w.SSIDs = out
	if w.Prefer {
		w.Enabled = true
	}
}

// rotatesUsername reports whether a username is generated per login.
func (c *Config) rotatesUsername() bool {
	return c.UsernameRotation != nil && c.UsernameRotation.Enabled
}

// description is a short human-readable summary for status and logs.
func (r *UsernameRotation) description() string {
	if r == nil || !r.Enabled {
		return ""
	}
	digits := r.digitsOrDefault()
	return fmt.Sprintf("random last %d digits (%0*d-%0*d)", digits, digits, r.Min, digits, r.Max)
}

func (r *UsernameRotation) validate(username string) error {
	if r == nil || !r.Enabled {
		return nil
	}
	digits := r.digitsOrDefault()
	if digits < 1 || digits > maxSuffixDigits {
		return fmt.Errorf("username_rotation.digits must be between 1 and %d (got %d)", maxSuffixDigits, digits)
	}
	if strings.TrimSpace(username) == "" {
		if !r.SuffixOnly {
			return errors.New("username is empty; set username, or set username_rotation.suffix_only for generated usernames")
		}
	} else {
		if r.SuffixOnly {
			return errors.New("username_rotation.suffix_only is only valid when username is empty")
		}
		if !hasTrailingDigits(username, digits) {
			return fmt.Errorf("username_rotation: username %q must end with at least %d digits", username, digits)
		}
	}
	limit := pow10(digits)
	if r.Min < 1 {
		return fmt.Errorf("username_rotation.min must be at least 1 (got %d)", r.Min)
	}
	if r.Max <= 0 {
		return errors.New("username_rotation.max is required and must be greater than 0")
	}
	if r.Max < r.Min {
		return fmt.Errorf("username_rotation.max (%d) must be >= min (%d)", r.Max, r.Min)
	}
	if r.Max >= limit {
		return fmt.Errorf("username_rotation.max must be less than %d for %d digits (got %d)", limit, digits, r.Max)
	}
	return nil
}

func (c *Config) validate() error {
	if c.Username == "" && !c.rotatesUsername() {
		return errors.New("username is empty (set username or enable username_rotation)")
	}
	if c.Password == "" {
		return errors.New("password is empty")
	}
	if c.LoginAttempts < 1 || c.LoginAttempts > 10 {
		return fmt.Errorf("login_attempts must be between 1 and 10 (got %d)", c.LoginAttempts)
	}
	if c.LoginSettleSeconds < 2 || c.LoginSettleSeconds > 30 {
		return fmt.Errorf("login_settle_seconds must be between 2 and 30 (got %d)", c.LoginSettleSeconds)
	}
	if err := c.UsernameRotation.validate(c.Username); err != nil {
		return err
	}
	if len(c.PortalHosts) == 0 {
		return errors.New("portal_hosts is empty")
	}
	for _, h := range c.PortalHosts {
		if strings.TrimSpace(h) == "" {
			return errors.New("portal_hosts contains an empty entry")
		}
	}
	if c.IntervalSeconds < 5 {
		return fmt.Errorf("interval_seconds must be at least 5 (got %d)", c.IntervalSeconds)
	}
	if c.IntervalSeconds > 86400 {
		return fmt.Errorf("interval_seconds must be at most 86400 (got %d)", c.IntervalSeconds)
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

func (c *Config) loginSettle() time.Duration {
	return time.Duration(c.LoginSettleSeconds) * time.Second
}

func (c *Config) loginAttempts() int {
	if c.LoginAttempts < 1 {
		return defaultLoginAttempts
	}
	return c.LoginAttempts
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

// readConfig parses a config without validating it, so commands can repair an
// existing file (for example one with an empty username) by merging new flags
// into it and saving the result.
func readConfig(path string) (*Config, error) {
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
	c.migrateLegacy()
	return &c, nil
}

// migrateLegacy upgrades configs written before suffix-only usernames became
// an explicit opt-in. Those configs stored an empty username with rotation
// enabled, which meant "generate the whole username". Treating that as
// username_rotation.suffix_only keeps an upgrade from breaking a working daemon;
// new configs still have to pass --random-only.
func (c *Config) migrateLegacy() {
	if strings.TrimSpace(c.Username) != "" || c.UsernameRotation == nil {
		return
	}
	if c.UsernameRotation.Enabled && !c.UsernameRotation.SuffixOnly {
		c.UsernameRotation.SuffixOnly = true
	}
}

func loadConfig(path string) (*Config, error) {
	c, err := readConfig(path)
	if err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
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
