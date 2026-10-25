package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	p := authMethod{Name: "radius", AuthType: 2}.payload(cfg, ctx)
	out := maskedPayload(p)
	if strings.Contains(out, "hunter2") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, `"username":"u"`) {
		t.Fatalf("username missing: %s", out)
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
	cfg.Username = "test-user"
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
}
