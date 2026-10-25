package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const liveURL = "https://192.168.1.3:8843/portal/entry?cid=AA:BB:CC:DD:EE:FF&ap=11:22:33:44:55:66&ssid=TestWiFi&t=1700000000&rid=1&u=example.com%2F"

func TestParsePortalURL(t *testing.T) {
	ctx, err := parsePortalURL(liveURL, []string{"192.168.1.3"})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Origin != "https://192.168.1.3:8843" {
		t.Errorf("origin = %q", ctx.Origin)
	}
	if ctx.ClientMAC != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("client = %q", ctx.ClientMAC)
	}
	if ctx.APMAC != "11:22:33:44:55:66" {
		t.Errorf("ap = %q", ctx.APMAC)
	}
	if ctx.SSID != "TestWiFi" {
		t.Errorf("ssid = %q", ctx.SSID)
	}
	if ctx.RadioID != "1" {
		t.Errorf("radio = %q", ctx.RadioID)
	}
	if ctx.OriginURL != "http://example.com/" {
		t.Errorf("origin url = %q", ctx.OriginURL)
	}
}

func TestParsePortalURLRejectsOtherHosts(t *testing.T) {
	if _, err := parsePortalURL(liveURL, []string{"10.0.0.1"}); err == nil {
		t.Fatal("expected host rejection")
	}
	bad := strings.Replace(liveURL, "cid=AA:BB:CC:DD:EE:FF", "cid=", 1)
	if _, err := parsePortalURL(bad, []string{"192.168.1.3"}); err == nil {
		t.Fatal("expected missing client MAC rejection")
	}
}

func TestExtractPortalURL(t *testing.T) {
	base := "http://example.com/"
	cases := map[string]string{
		"js escaped": `window.location.href="https:\/\/192.168.1.3:8843\/portal\/entry?cid=AA&ap=BB&amp;ssid=GEC";`,
		"meta":       `<meta http-equiv="refresh" content="0; url=/portal/entry?cid=AA&ap=BB&ssid=GEC">`,
		"relative":   `<script>location.replace('/portal/entry?cid=AA&ap=BB&ssid=GEC')</script>`,
		"plain":      `<a href="https://192.168.1.3:8843/portal/entry?cid=AA&ap=BB">login</a>`,
	}
	for name, body := range cases {
		got := extractPortalURL(body, base)
		if !strings.Contains(got, "/portal/entry") || !strings.Contains(got, "cid=AA") {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

func TestResolveAuthMethod(t *testing.T) {
	cases := []struct {
		authType int
		name     string
		endpoint string
		ok       bool
	}{
		{2, "radius", "/portal/radius/auth", true},
		{8, "radius", "/portal/radius/auth", true},
		{5, "local", "/portal/auth", true},
		{1, "simple", "/portal/auth", true},
		{0, "none", "/portal/auth", true},
		{3, "", "", false},
		{6, "", "", false},
		{99, "", "", false},
	}
	for _, tc := range cases {
		m, err := resolveAuthMethod(settingResult{AuthType: tc.authType})
		if tc.ok && err != nil {
			t.Errorf("authType %d: %v", tc.authType, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("authType %d: expected error", tc.authType)
			}
			continue
		}
		if m.Name != tc.name || m.Endpoint != tc.endpoint {
			t.Errorf("authType %d: got %s %s", tc.authType, m.Name, m.Endpoint)
		}
	}
}

func TestHotspotModePicksLocalOrRadius(t *testing.T) {
	r := settingResult{AuthType: 11}
	r.Hotspot.EnabledTypes = []int{6, 8}
	m, err := resolveAuthMethod(r)
	if err != nil || m.Name != "radius" {
		t.Fatalf("got %v %v", m, err)
	}
	r.Hotspot.EnabledTypes = []int{3}
	if _, err := resolveAuthMethod(r); err == nil {
		t.Fatal("expected voucher rejection")
	}
}

func TestMaskedPayloadHidesSecrets(t *testing.T) {
	cfg := &Config{Username: "u", Password: "hunter2"}
	ctx, err := parsePortalURL(liveURL, []string{"192.168.1.3"})
	if err != nil {
		t.Fatal(err)
	}
	p := authMethod{Name: "radius", AuthType: 2}.payload(cfg, ctx, cfg.Username)
	out := maskedPayload(p)
	if strings.Contains(out, "hunter2") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, `"username":"u"`) {
		t.Fatalf("username missing: %s", out)
	}
}

func TestUsernameRotationPick(t *testing.T) {
	r := &UsernameRotation{Enabled: true, Digits: 3, Min: 1, Max: 112}
	if got := r.pickN("22BCS12345", 0); got != "22BCS12001" {
		t.Fatalf("first value = %q", got)
	}
	if got := r.pickN("22BCS12345", 111); got != "22BCS12112" {
		t.Fatalf("last value = %q", got)
	}
	for i := 0; i < 100; i++ {
		got := r.pick("22BCS12345")
		if !strings.HasPrefix(got, "22BCS12") || len(got) != len("22BCS12345") {
			t.Fatalf("unexpected username %q", got)
		}
		n, err := strconv.Atoi(got[len(got)-3:])
		if err != nil || n < 1 || n > 112 {
			t.Fatalf("suffix out of range in %q", got)
		}
	}
}

func TestDoLoginDryRunUsesRotatedUsername(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/portal/getPortalPageSetting" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errorCode":0,"result":{"authType":5}}`))
	}))
	defer srv.Close()

	portalURL := srv.URL + "/portal/entry?cid=AA:BB:CC:DD:EE:FF&ap=11:22:33:44:55:66&ssid=TestWiFi&rid=1"
	ctx, err := parsePortalURL(portalURL, []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Username:         "22BCS12345",
		Password:         "test-pass",
		PortalHosts:      []string{"127.0.0.1"},
		TimeoutSeconds:   3,
		UsernameRotation: &UsernameRotation{Enabled: true, Digits: 3, Min: 1, Max: 112},
	}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	dryErr := doLogin(cfg, ctx, true, nil)
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	_ = r.Close()
	if dryErr != nil {
		t.Fatal(dryErr)
	}

	text := string(out)
	if !strings.Contains(text, "authType:   5 (local)") {
		t.Fatalf("missing auth method in output:\n%s", text)
	}
	if !strings.Contains(text, "rotation:   random last 3 digits (001-112)") {
		t.Fatalf("missing rotation summary in output:\n%s", text)
	}
	var picked string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "username:") {
			picked = strings.TrimSpace(strings.TrimPrefix(line, "username:"))
		}
	}
	if !strings.HasPrefix(picked, "22BCS12") || len(picked) != len("22BCS12345") {
		t.Fatalf("unexpected username in output: %q\n%s", picked, text)
	}
	n, err := strconv.Atoi(picked[len(picked)-3:])
	if err != nil || n < 1 || n > 112 {
		t.Fatalf("suffix out of range in %q", picked)
	}
}

func TestLoginUsernameRotationOff(t *testing.T) {
	cfg := &Config{Username: "22BCS12345"}
	if got := cfg.loginUsername(); got != cfg.Username {
		t.Fatalf("got %q", got)
	}
	cfg.UsernameRotation = &UsernameRotation{Enabled: false, Digits: 3, Min: 1, Max: 112}
	if got := cfg.loginUsername(); got != cfg.Username {
		t.Fatalf("disabled rotation changed username to %q", got)
	}
}

func TestUsernameRotationDefaults(t *testing.T) {
	r := &UsernameRotation{Enabled: true, Min: 1, Max: 112}
	r.applyDefaults()
	if r.Digits != 3 {
		t.Fatalf("digits = %d", r.Digits)
	}
}

func TestUsernameRotationValidation(t *testing.T) {
	valid := &UsernameRotation{Enabled: true, Digits: 3, Min: 1, Max: 112}
	if err := valid.validate("22BCS12345"); err != nil {
		t.Fatalf("valid rotation rejected: %v", err)
	}

	cases := []struct {
		name     string
		r        *UsernameRotation
		username string
	}{
		{"max required", &UsernameRotation{Enabled: true, Digits: 3, Min: 1}, "22BCS12345"},
		{"max below min", &UsernameRotation{Enabled: true, Digits: 3, Min: 112, Max: 1}, "22BCS12345"},
		{"max too large", &UsernameRotation{Enabled: true, Digits: 3, Min: 1, Max: 1000}, "22BCS12345"},
		{"min negative", &UsernameRotation{Enabled: true, Digits: 3, Min: -1, Max: 10}, "22BCS12345"},
		{"bad suffix", &UsernameRotation{Enabled: true, Digits: 3, Min: 1, Max: 112}, "22BCS12abc"},
		{"short username", &UsernameRotation{Enabled: true, Digits: 3, Min: 1, Max: 112}, "12"},
		{"digits too large", &UsernameRotation{Enabled: true, Digits: 10, Min: 0, Max: 1}, "1234567890"},
		{"digits negative", &UsernameRotation{Enabled: true, Digits: -1, Min: 1, Max: 10}, "22BCS12345"},
	}
	for _, tc := range cases {
		if err := tc.r.validate(tc.username); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}

	disabled := &UsernameRotation{Enabled: false, Digits: 99, Min: -1, Max: -1}
	if err := disabled.validate("user"); err != nil {
		t.Fatalf("disabled rotation should skip validation: %v", err)
	}
}

func TestConfigUsernameRotationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := defaultConfig()
	cfg.Username = "22BCS12345"
	cfg.Password = "test-pass"
	cfg.UsernameRotation = &UsernameRotation{Enabled: true, Digits: 3, Min: 1, Max: 112}
	if err := saveConfig(path, &cfg); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsernameRotation == nil {
		t.Fatal("username_rotation missing after round trip")
	}
	if *got.UsernameRotation != *cfg.UsernameRotation {
		t.Fatalf("rotation mismatch: got %+v, want %+v", *got.UsernameRotation, *cfg.UsernameRotation)
	}
}

func TestProbeOnline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.CheckURLs = []string{srv.URL + "/generate_204"}
	res := probe(&cfg, 3*time.Second)
	if !res.Online {
		t.Fatalf("expected online, got %+v", res)
	}
}

func TestProbeCaptivePortal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><script>window.location.href="` + liveURL + `";</script></html>`))
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.CheckURLs = []string{srv.URL + "/"}
	res := probe(&cfg, 3*time.Second)
	if res.Online {
		t.Fatal("captive page must not count as online")
	}
	if res.Context == nil || res.Context.SSID != "TestWiFi" {
		t.Fatalf("portal context not parsed: %+v", res)
	}
}

func TestGenerate204With200IsCaptive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A captive gateway answering 200 for a URL that should be 204.
		_, _ = w.Write([]byte("login"))
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.CheckURLs = []string{srv.URL + "/generate_204"}
	res := probe(&cfg, 3*time.Second)
	if res.Online {
		t.Fatalf("200 for generate_204 must not count as online: %+v", res)
	}
}

func TestProbeRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, liveURL, http.StatusFound)
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.CheckURLs = []string{srv.URL + "/"}
	res := probe(&cfg, 3*time.Second)
	if res.Context == nil {
		t.Fatalf("redirect not parsed: %+v", res)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := defaultConfig()
	cfg.Username = "test-user001"
	cfg.Password = "test-pass"
	if err := saveConfig(path, &cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("config is too permissive: %o", info.Mode().Perm())
	}
	got, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != cfg.Username || got.Password != cfg.Password {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestFlexInt(t *testing.T) {
	var v struct {
		N flexInt `json:"n"`
	}
	if err := json.Unmarshal([]byte(`{"n":"0"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.N != 0 {
		t.Fatalf("got %d", v.N)
	}
	if err := json.Unmarshal([]byte(`{"n":-41525}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.N != -41525 {
		t.Fatalf("got %d", v.N)
	}
}

func TestSystemdQuote(t *testing.T) {
	if got := systemdQuote("/usr/local/bin/omada-autologin"); got != "/usr/local/bin/omada-autologin" {
		t.Errorf("plain path = %q", got)
	}
	got := systemdQuote(`/home/a b/omada`)
	if got != `"/home/a b/omada"` {
		t.Errorf("spaced path = %q", got)
	}
	got = systemdQuote(`/tmp/a"b`)
	if got != `"/tmp/a\"b"` {
		t.Errorf("quoted path = %q", got)
	}
	got = systemdQuote(`/tmp/100%/data`)
	if got != `"/tmp/100%%/data"` {
		t.Errorf("specifier path = %q", got)
	}
	got = systemdQuote(`/tmp/$HOME/bin`)
	if got != `"/tmp/$$HOME/bin"` {
		t.Errorf("variable path = %q", got)
	}
}

func TestConfigBackoffValidation(t *testing.T) {
	base := defaultConfig()
	base.Username = "user001"
	base.Password = "pass"
	base.applyDefaults()
	if err := base.validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	if base.RetryMinSeconds != 5 || base.RetryMaxSeconds != 60 {
		t.Fatalf("defaults: min=%d max=%d", base.RetryMinSeconds, base.RetryMaxSeconds)
	}

	cases := []struct {
		name string
		edit func(*Config)
	}{
		{"min too low", func(c *Config) { c.RetryMinSeconds = 1 }},
		{"max below min", func(c *Config) { c.RetryMinSeconds = 30; c.RetryMaxSeconds = 10 }},
		{"max too high", func(c *Config) { c.RetryMaxSeconds = 3601 }},
	}
	for _, tc := range cases {
		c := base
		tc.edit(&c)
		if err := c.validate(); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

func TestConfigBackoffDefaults(t *testing.T) {
	var c Config
	c.applyDefaults()
	if c.RetryMinSeconds != 5 || c.RetryMaxSeconds != 60 || c.IntervalSeconds != 60 {
		t.Fatalf("got interval=%d min=%d max=%d", c.IntervalSeconds, c.RetryMinSeconds, c.RetryMaxSeconds)
	}
}

func TestNextDelay(t *testing.T) {
	cfg := defaultConfig()
	cfg.IntervalSeconds = 60
	cfg.RetryMinSeconds = 5
	cfg.RetryMaxSeconds = 60
	a := &app{cfg: &cfg}

	delay, next := a.nextDelay(checkOutcome{Online: true}, nil, 20*time.Second)
	if delay != 60*time.Second || next != 5*time.Second {
		t.Fatalf("online: delay=%s next=%s", delay, next)
	}

	delay, next = a.nextDelay(checkOutcome{}, errors.New("offline"), 5*time.Second)
	if delay != 5*time.Second || next != 10*time.Second {
		t.Fatalf("first retry: delay=%s next=%s", delay, next)
	}
	delay, next = a.nextDelay(checkOutcome{}, errors.New("offline"), 40*time.Second)
	if delay != 40*time.Second || next != 60*time.Second {
		t.Fatalf("capped retry: delay=%s next=%s", delay, next)
	}
	delay, next = a.nextDelay(checkOutcome{Skipped: true}, nil, 60*time.Second)
	if delay != 60*time.Second || next != 60*time.Second {
		t.Fatalf("skipped: delay=%s next=%s", delay, next)
	}
}
