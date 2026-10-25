package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PortalContext is everything the Omada controller needs to authenticate the
// client that was redirected to the captive portal.
type PortalContext struct {
	PortalURL  string
	Origin     string
	Host       string
	ClientMAC  string
	APMAC      string
	GatewayMAC string
	SSID       string
	RadioID    string
	VID        string
	OriginURL  string
}

func (p *PortalContext) validate(allowedHosts []string) error {
	if p == nil {
		return errors.New("empty portal context")
	}
	u, err := url.Parse(p.PortalURL)
	if err != nil {
		return fmt.Errorf("parse portal URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("portal URL scheme %q is not http(s)", u.Scheme)
	}
	if !hostAllowed(u, allowedHosts) {
		return fmt.Errorf("refusing to send credentials to %q (not in portal_hosts)", u.Host)
	}
	if strings.TrimSpace(p.ClientMAC) == "" {
		return errors.New("portal URL has no client MAC (cid)")
	}
	if strings.TrimSpace(p.APMAC) == "" && strings.TrimSpace(p.GatewayMAC) == "" {
		return errors.New("portal URL has neither an AP MAC (ap) nor a gateway MAC")
	}
	return nil
}

func hostAllowed(u *url.URL, allowed []string) bool {
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	for _, entry := range allowed {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if h, _, ok := strings.Cut(entry, "/"); ok {
			entry = h
		}
		if host == entry || strings.EqualFold(u.Host, entry) {
			return true
		}
	}
	return false
}

// parsePortalURL extracts the context from an Omada entry URL such as:
//
//	https://192.168.1.3:8843/portal/entry?cid=...&ap=...&ssid=...&t=...&rid=1&u=...
func parsePortalURL(raw string, allowedHosts []string) (*PortalContext, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, `\/`, `/`)
	raw = strings.ReplaceAll(raw, "&amp;", "&")
	raw = strings.ReplaceAll(raw, `\u0026`, "&")

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse portal URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("portal URL scheme %q is not http(s)", u.Scheme)
	}
	if !strings.Contains(u.Path, "/portal/") {
		return nil, fmt.Errorf("%q is not an Omada portal URL", raw)
	}
	if !hostAllowed(u, allowedHosts) {
		return nil, fmt.Errorf("refusing portal host %q (not in portal_hosts)", u.Host)
	}

	q := u.Query()
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(q.Get(k)); v != "" {
				return v
			}
		}
		return ""
	}

	ctx := &PortalContext{
		PortalURL:  raw,
		Origin:     u.Scheme + "://" + u.Host,
		Host:       u.Hostname(),
		ClientMAC:  first("cid", "clientMac", "client_mac"),
		APMAC:      first("ap", "apMac", "ap_mac"),
		GatewayMAC: first("gateway", "gatewayMac", "gateway_mac"),
		SSID:       first("ssid", "ssidName", "ssid_name"),
		RadioID:    numeric(first("rid", "radioId", "radio_id")),
		VID:        numeric(first("vid")),
		OriginURL:  first("u", "originUrl", "origin_url"),
	}
	if ctx.OriginURL != "" && !strings.HasPrefix(ctx.OriginURL, "http://") && !strings.HasPrefix(ctx.OriginURL, "https://") {
		ctx.OriginURL = "http://" + ctx.OriginURL
	}
	if err := ctx.validate(allowedHosts); err != nil {
		return nil, err
	}
	return ctx, nil
}

func numeric(s string) string {
	if s == "" {
		return ""
	}
	if _, err := strconv.Atoi(s); err != nil {
		return ""
	}
	return s
}

// withClientMAC returns a copy refreshed for the current interface MAC and
// timestamp. Cached Omada entry URLs embed the client MAC (cid) and were often
// issued for a previous private Wi-Fi address; reusing them makes the portal
// reject the login with "invalid client".
func (p *PortalContext) withClientMAC(mac string, now time.Time) *PortalContext {
	if p == nil {
		return nil
	}
	cp := *p
	u, err := url.Parse(cp.PortalURL)
	if err != nil {
		return &cp
	}
	q := u.Query()
	if mac != "" {
		cp.ClientMAC = normalizeMAC(mac)
		set := false
		for _, key := range []string{"cid", "clientMac", "client_mac"} {
			if q.Has(key) {
				q.Set(key, cp.ClientMAC)
				set = true
			}
		}
		if !set {
			q.Set("cid", cp.ClientMAC)
		}
	}
	q.Set("t", strconv.FormatInt(now.Unix(), 10))
	u.RawQuery = q.Encode()
	cp.PortalURL = u.String()
	return &cp
}

// contextFields returns the JSON fields shared by getPortalPageSetting and
// the authentication endpoints.
func (p *PortalContext) contextFields() map[string]any {
	m := map[string]any{}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("clientMac", p.ClientMAC)
	set("apMac", p.APMAC)
	set("gatewayMac", p.GatewayMAC)
	set("ssidName", p.SSID)
	set("originUrl", p.OriginURL)
	if n, err := strconv.Atoi(p.RadioID); err == nil {
		m["radioId"] = n
	}
	if n, err := strconv.Atoi(p.VID); err == nil {
		m["vid"] = n
	}
	return m
}

func newDiscoveryClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		// Inspect the gateway's redirect rather than following it off-network.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newPortalClient(timeout time.Duration, allowedHosts []string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Omada controllers ship a self-signed certificate. Portal hosts are
	// allow-listed above; no other host ever receives credentials.
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		// A 307/308 replays the request body, so following a redirect blindly
		// would forward the password to whatever host it names. Keep every hop
		// inside portal_hosts.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !hostAllowed(req.URL, allowedHosts) {
				return fmt.Errorf("portal redirected to %s, which is not in portal_hosts", req.URL.Host)
			}
			if len(via) >= 5 {
				return errors.New("too many portal redirects")
			}
			return nil
		},
	}
}
