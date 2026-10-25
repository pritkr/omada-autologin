package main

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxProbeBody = 1 << 20

// probeResult is the outcome of one connectivity probe.
type probeResult struct {
	Online     bool
	Context    *PortalContext
	CheckedURL string
	StatusCode int
	FoundURL   string
	Detail     string
}

var (
	portalURLRe      = regexp.MustCompile(`(?i)https?://[^\s"'<>\\]+/portal/[^\s"'<>\\]*`)
	relativePortalRe = regexp.MustCompile(`(?i)["'](/portal/[^"']+)["']`)
	metaURLRe        = regexp.MustCompile(`(?i)content\s*=\s*["'][^"']*?url\s*=\s*([^"'>\s]+)`)
)

// probe checks the configured URLs and classifies the network as online,
// captive (with a portal context when one is parseable), or unknown.
func probe(cfg *Config, timeout time.Duration) probeResult {
	client := newDiscoveryClient(timeout)
	defer client.CloseIdleConnections()

	res := probeResult{}
	for _, raw := range cfg.CheckURLs {
		res.CheckedURL = raw
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			res.Detail = err.Error()
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; "+appName+")")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		req.Header.Set("Cache-Control", "no-cache")

		resp, err := client.Do(req)
		if err != nil {
			res.Detail = err.Error()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
		_ = resp.Body.Close()
		res.StatusCode = resp.StatusCode

		// The gateway normally answers with 302 + Location. Captive portals
		// that answer 200 in place put the URL in a script or meta refresh.
		if loc := resp.Header.Get("Location"); loc != "" {
			if cand := resolveReference(raw, loc); cand != "" {
				res.FoundURL = cand
				if ctx := tryPortalURL(cand, cfg.PortalHosts); ctx != nil {
					res.Context = ctx
					return res
				}
			}
		}
		if cand := extractPortalURL(string(body), raw); cand != "" {
			res.FoundURL = cand
			if ctx := tryPortalURL(cand, cfg.PortalHosts); ctx != nil {
				res.Context = ctx
				return res
			}
		}

		if hasPortalMarkers(string(body)) {
			res.Detail = "captive portal page without a usable entry URL"
			return res
		}

		if responseIsOnline(raw, resp.StatusCode, body) {
			res.Online = true
			return res
		}
	}
	return res
}

// probeFresh repeats the captive check with a unique query parameter so a
// gateway or local cache cannot answer with the previous probe's result. This
// is used when a cached portal URL has gone stale (for example after macOS
// rotated the private Wi-Fi MAC).
func probeFresh(cfg *Config, timeout time.Duration) probeResult {
	c := *cfg
	c.CheckURLs = make([]string, 0, len(cfg.CheckURLs))
	buster := strconv.FormatInt(time.Now().UnixNano(), 36)
	for _, raw := range cfg.CheckURLs {
		u, err := url.Parse(raw)
		if err != nil {
			c.CheckURLs = append(c.CheckURLs, raw)
			continue
		}
		q := u.Query()
		q.Set("_omada", buster)
		u.RawQuery = q.Encode()
		c.CheckURLs = append(c.CheckURLs, u.String())
	}
	return probe(&c, timeout)
}

// responseIsOnline is deliberately conservative. A login page served with
// HTTP 200 must not be mistaken for internet access. The well-known
// connectivity URLs are checked for their expected response; custom URLs are
// accepted when they return a non-empty 2xx page without portal markers.
func responseIsOnline(rawURL string, status int, body []byte) bool {
	if status == http.StatusNoContent {
		return true
	}
	if status < 200 || status >= 300 {
		return false
	}
	text := strings.ToLower(string(body))
	u := strings.ToLower(rawURL)
	switch {
	case strings.Contains(u, "example.com"):
		return strings.Contains(text, "example domain")
	case strings.Contains(u, "captive.apple.com"):
		return strings.Contains(text, "<title>success</title>")
	case strings.Contains(u, "generate_204"):
		// generate_204 should answer 204; a 200 here is a captive page.
		return status == http.StatusNoContent
	default:
		return len(body) > 0 && !hasPortalMarkers(text)
	}
}

func hasPortalMarkers(s string) bool {
	s = strings.ToLower(s)
	for _, marker := range []string{
		"/portal/",
		"getportalpagesetting",
		"omada",
		"tp-link",
		"captive portal",
		"login required",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// extractPortalURL finds an Omada entry URL in a redirect body, a JS snippet,
// or a meta refresh. Escaped slashes and HTML entities are normalised first.
func extractPortalURL(body, base string) string {
	body = strings.ReplaceAll(body, `\/`, `/`)
	body = strings.ReplaceAll(body, "&amp;", "&")
	body = strings.ReplaceAll(body, `\u0026`, "&")

	for _, m := range portalURLRe.FindAllString(body, -1) {
		m = trimURLPunctuation(m)
		if m != "" {
			return m
		}
	}
	for _, m := range metaURLRe.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 {
			if cand := resolveReference(base, m[1]); cand != "" {
				return cand
			}
		}
	}
	for _, m := range relativePortalRe.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 {
			if cand := resolveReference(base, m[1]); cand != "" {
				return cand
			}
		}
	}
	return ""
}

func trimURLPunctuation(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, `);,.`)
	return s
}

func resolveReference(base, ref string) string {
	ref = strings.TrimSpace(strings.ReplaceAll(ref, "&amp;", "&"))
	ref = strings.Trim(ref, `"'`)
	if ref == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return b.ResolveReference(r).String()
}

func tryPortalURL(raw string, allowed []string) *PortalContext {
	ctx, err := parsePortalURL(raw, allowed)
	if err != nil {
		return nil
	}
	return ctx
}
