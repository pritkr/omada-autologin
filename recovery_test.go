package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithClientMACRefreshesCIDAndTimestamp(t *testing.T) {
	ctx, err := parsePortalURL(liveURL, []string{"192.168.1.3"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	got := ctx.withClientMAC("5a:02:e1:dc:3b:f0", now)
	if got == ctx {
		t.Fatal("withClientMAC should return a copy")
	}
	if got.ClientMAC != "5A:02:E1:DC:3B:F0" {
		t.Fatalf("client mac = %q", got.ClientMAC)
	}
	if !strings.Contains(got.PortalURL, "cid=5A%3A02%3AE1%3ADC%3A3B%3AF0") {
		t.Fatalf("portal URL does not carry the new cid: %s", got.PortalURL)
	}
	u, err := url.Parse(got.PortalURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("t"); got != "1800000000" {
		t.Fatalf("t = %q, want 1800000000", got)
	}
	if u.Query().Get("ap") != "11:22:33:44:55:66" || u.Query().Get("ssid") != "TestWiFi" {
		t.Fatalf("withClientMAC dropped other portal fields: %s", got.PortalURL)
	}
	if ctx.ClientMAC != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("original context was mutated: %q", ctx.ClientMAC)
	}
}

func TestCachedPortalPrefersStateURL(t *testing.T) {
	dir := t.TempDir()
	st, err := newStateStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	stateURL := strings.Replace(liveURL, "TestWiFi", "StateNet", 1) + "&src=state"
	if err := st.save(State{LastPortalURL: stateURL}); err != nil {
		t.Fatal(err)
	}
	cfgURL := strings.Replace(liveURL, "TestWiFi", "ConfigNet", 1) + "&src=config"
	a := &app{
		cfg:   &Config{PortalHosts: []string{"192.168.1.3"}, PortalURL: cfgURL},
		state: st,
	}
	ctx := a.cachedPortal()
	if ctx == nil {
		t.Fatal("no cached portal")
	}
	u, err := url.Parse(ctx.PortalURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("src"); got != "state" {
		t.Fatalf("source = %q, want state", got)
	}
	if got := u.Query().Get("ssid"); got != "StateNet" {
		t.Fatalf("ssid = %q, want StateNet", got)
	}
}

func TestWifiTargetsDedupAndLearnLastSSID(t *testing.T) {
	cfg := &Config{WiFi: &WiFiConfig{Enabled: true, SSIDs: []string{"Office", " Home ", "Office", ""}}}
	cfg.applyDefaults()
	got := wifiTargets(cfg, State{LastSSID: "Cafe"})
	want := []string{"Office", "Home", "Cafe"}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("targets = %v, want %v", got, want)
		}
	}
	if extra := wifiTargets(cfg, State{LastSSID: "Office"}); len(extra) != 2 {
		t.Fatalf("learned SSID should not duplicate a configured one: %v", extra)
	}
}

func TestWifiTargetsDisabled(t *testing.T) {
	cfg := &Config{WiFi: &WiFiConfig{Enabled: false, SSIDs: []string{"Office"}}}
	if got := wifiTargets(cfg, State{LastSSID: "Home"}); got != nil {
		t.Fatalf("disabled wifi returned targets: %v", got)
	}
}

func TestConfigRejectsEmptyUsernameWithoutSuffixOnly(t *testing.T) {
	cfg := defaultConfig()
	cfg.Password = "test-pass"
	if err := cfg.validate(); err == nil {
		t.Fatal("empty username with prefixed rotation should fail validation")
	}
	cfg.UsernameRotation.SuffixOnly = true
	if err := cfg.validate(); err != nil {
		t.Fatalf("explicit suffix_only should validate: %v", err)
	}
}

func TestConfigRejectsSuffixOnlyWithPrefix(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = "24105157001"
	cfg.Password = "test-pass"
	cfg.UsernameRotation.SuffixOnly = true
	if err := cfg.validate(); err == nil {
		t.Fatal("suffix_only with a username prefix should fail validation")
	}
}

func TestRandomizePreservesSuffixOnly(t *testing.T) {
	defer func() {
		flagConfig = ""
		verbose = false
	}()

	path := filepath.Join(t.TempDir(), "config.json")
	cfg := defaultConfig()
	cfg.Password = "test-pass"
	cfg.UsernameRotation.SuffixOnly = true
	if err := saveConfig(path, &cfg); err != nil {
		t.Fatal(err)
	}
	if code := runCLI([]string{"randomize", "--config", path, "--range", "001-050"}); code != 0 {
		t.Fatalf("randomize exit = %d", code)
	}
	got, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "" || !got.UsernameRotation.SuffixOnly {
		t.Fatalf("randomize dropped suffix-only mode: %+v", got.UsernameRotation)
	}
	if got.UsernameRotation.Min != 1 || got.UsernameRotation.Max != 50 {
		t.Fatalf("range = %d-%d", got.UsernameRotation.Min, got.UsernameRotation.Max)
	}
}

func TestConfigValidatesRetrySettings(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = "24105157001"
	cfg.Password = "test-pass"
	cfg.LoginAttempts = 0
	cfg.LoginSettleSeconds = 0
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}

	cfg.LoginAttempts = 11
	if err := cfg.validate(); err == nil {
		t.Fatal("login_attempts above the maximum should fail")
	}
	cfg.LoginAttempts = 3
	cfg.LoginSettleSeconds = 1
	if err := cfg.validate(); err == nil {
		t.Fatal("login_settle_seconds below the minimum should fail")
	}
}

func TestLoginErrorClassification(t *testing.T) {
	base := errTest("boom")
	if isRetryableLoginError(base) {
		t.Fatal("plain error should not be retryable")
	}
	wrapped := markRetryable(base, true)
	if !isRetryableLoginError(wrapped) {
		t.Fatal("marked error should be retryable")
	}
	if !shouldRefreshPortal(wrapped) {
		t.Fatal("marked error should request a portal refresh")
	}
	if wrapped.Error() != base.Error() {
		t.Fatalf("wrapped message = %q, want %q", wrapped.Error(), base.Error())
	}
	if shouldRefreshPortal(markRetryable(base, false)) {
		t.Fatal("error should not request a refresh")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestAttemptRetriesAnotherUsername(t *testing.T) {
	var authCalls atomic.Int32
	portal := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/portal/getPortalPageSetting":
			_, _ = w.Write([]byte(`{"errorCode":0,"result":{"authType":5}}`))
		case "/portal/auth":
			if authCalls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"errorCode":-41527,"msg":"invalid local user"}`))
				return
			}
			_, _ = w.Write([]byte(`{"errorCode":0,"msg":"success."}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer portal.Close()

	online := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer online.Close()

	ctx, err := parsePortalURL(portal.URL+"/portal/entry?cid=AA:BB:CC:DD:EE:FF&ap=11:22:33:44:55:66&ssid=TestWiFi&rid=1&t=1", []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st, err := newStateStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	log, err := newLogger("", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Username = "24105157001"
	cfg.Password = "test-pass"
	cfg.PortalHosts = []string{"127.0.0.1"}
	cfg.CheckURLs = []string{online.URL}
	cfg.LoginAttempts = 2
	cfg.LoginSettleSeconds = 2
	a := &app{cfg: &cfg, dir: dir, state: st, log: log}

	attempted, err := a.attempt(ctx, onceOptions{})
	if err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if !attempted {
		t.Fatal("attempt reported no login")
	}
	if got := authCalls.Load(); got != 2 {
		t.Fatalf("auth calls = %d, want 2", got)
	}
	state := st.load()
	if state.FailCount != 0 || state.LastSuccess == 0 {
		t.Fatalf("success not recorded: %+v", state)
	}
	if state.LastSSID != "TestWiFi" {
		t.Fatalf("last SSID = %q", state.LastSSID)
	}
}

func TestAttemptStopsOnWrongPassword(t *testing.T) {
	var authCalls atomic.Int32
	portal := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/portal/getPortalPageSetting" {
			_, _ = w.Write([]byte(`{"errorCode":0,"result":{"authType":5}}`))
			return
		}
		authCalls.Add(1)
		_, _ = w.Write([]byte(`{"errorCode":-41525,"msg":"wrong password"}`))
	}))
	defer portal.Close()

	ctx, err := parsePortalURL(portal.URL+"/portal/entry?cid=AA:BB:CC:DD:EE:FF&ap=11:22:33:44:55:66&ssid=TestWiFi&rid=1&t=1", []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st, err := newStateStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Username = "24105157001"
	cfg.Password = "wrong"
	cfg.PortalHosts = []string{"127.0.0.1"}
	cfg.LoginAttempts = 3
	cfg.LoginSettleSeconds = 2
	a := &app{cfg: &cfg, dir: dir, state: st}

	if _, err := a.attempt(ctx, onceOptions{}); err == nil {
		t.Fatal("wrong password should fail")
	}
	if got := authCalls.Load(); got != 1 {
		t.Fatalf("auth calls = %d; wrong password must not spin through suffixes", got)
	}
	if state := st.load(); state.FailCount != 1 {
		t.Fatalf("fail count = %d, want 1", state.FailCount)
	}
}
