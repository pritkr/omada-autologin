package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// routeInfo is the current IPv4 default route.
type routeInfo struct {
	Interface string
	Gateway   string
}

// wifiStatus is the fast subset used on every daemon tick. SSID is populated
// separately because querying it is slow on macOS.
type wifiStatus struct {
	Interface string
	SSID      string
	IPv4      string
	Gateway   string
	MAC       string
	Primary   string
	LinkUp    bool
}

func (s wifiStatus) String() string {
	parts := []string{"iface=" + s.Interface}
	if s.SSID != "" {
		parts = append(parts, "ssid="+s.SSID)
	}
	if s.IPv4 != "" {
		parts = append(parts, "ip="+s.IPv4)
	}
	if s.Gateway != "" {
		parts = append(parts, "gw="+s.Gateway)
	}
	return strings.Join(parts, " ")
}

func normalizeMAC(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "-", ":"))
	if s == "" {
		return ""
	}
	if hw, err := net.ParseMAC(s); err == nil {
		return strings.ToUpper(hw.String())
	}
	return strings.ToUpper(s)
}

func sameMAC(a, b string) bool {
	a, b = normalizeMAC(a), normalizeMAC(b)
	return a != "" && a == b
}

// SSIDs are case-sensitive on the wire, but operators type them by hand and
// macOS sometimes reports a different case than the saved profile. Treat
// targets case-insensitively on this side; the configured spelling is what
// gets handed to the OS when connecting.
func sameSSID(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

func interfaceIPv4(name string) string {
	if name == "" {
		name = defaultRoute().Interface
	}
	if name == "" {
		name = wifiDefaultInterface()
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.String()
	}
	return ""
}

func interfaceMAC(name string) string {
	if name == "" {
		name = defaultRoute().Interface
	}
	if name == "" {
		name = wifiDefaultInterface()
	}
	iface, err := net.InterfaceByName(name)
	if err != nil || len(iface.HardwareAddr) == 0 {
		return ""
	}
	return normalizeMAC(iface.HardwareAddr.String())
}

// defaultLocalInterface finds the interface used for a routed connection
// without sending traffic. It is a fallback when the route table parser is
// unavailable.
func defaultLocalInterface() string {
	conn, err := net.DialTimeout("udp4", "1.1.1.1:53", 500*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	ip := local.IP.String()
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.String() == ip {
				return iface.Name
			}
		}
	}
	return ""
}

// interfaceNameByIPv4 maps a local address back to its interface. Windows'
// route table reports an interface address rather than a friendly name.
func interfaceNameByIPv4(ip string) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.String() == ip {
				return iface.Name
			}
		}
	}
	return ""
}

func wifiInterface(cfg *Config) string {
	if cfg != nil && cfg.WiFi != nil {
		if iface := strings.TrimSpace(cfg.WiFi.Interface); iface != "" {
			return iface
		}
	}
	// Prefer the actual wireless port over the default route: a docked laptop
	// often routes through Ethernet while still using the Wi-Fi MAC for the
	// captive portal.
	if iface := detectedWiFiInterface(); iface != "" {
		return iface
	}
	if iface := defaultRoute().Interface; iface != "" {
		return iface
	}
	return defaultLocalInterface()
}

var wifiIfaceCache struct {
	sync.Mutex
	name  string
	until time.Time
}

// detectedWiFiInterface caches the platform lookup. On macOS and Linux it is
// shelled out, and the daemon's 2-second watcher must stay cheap.
func detectedWiFiInterface() string {
	wifiIfaceCache.Lock()
	defer wifiIfaceCache.Unlock()
	if time.Now().Before(wifiIfaceCache.until) {
		// A radio toggle or a removed USB adapter invalidates the name even
		// before the cache expires.
		if wifiIfaceCache.name == "" {
			return ""
		}
		if _, err := net.InterfaceByName(wifiIfaceCache.name); err == nil {
			return wifiIfaceCache.name
		}
	}
	name := wifiDefaultInterface()
	wifiIfaceCache.name = name
	if name == "" {
		// A missing interface may be temporary (USB Wi-Fi, radio off).
		wifiIfaceCache.until = time.Now().Add(30 * time.Second)
	} else {
		wifiIfaceCache.until = time.Now().Add(5 * time.Minute)
	}
	return name
}

// currentWiFiStatus is deliberately fast: no SSID lookup, no system_profiler.
func currentWiFiStatus(cfg *Config) wifiStatus {
	iface := wifiInterface(cfg)
	r := defaultRoute()
	st := wifiStatus{
		Interface: iface,
		IPv4:      interfaceIPv4(iface),
		MAC:       interfaceMAC(iface),
		Primary:   r.Interface,
	}
	if r.Interface == "" || r.Interface == iface {
		st.Gateway = r.Gateway
	}
	st.LinkUp = st.IPv4 != ""
	return st
}

func currentWiFiStatusWithSSID(cfg *Config) wifiStatus {
	st := currentWiFiStatus(cfg)
	st.SSID = wifiCurrentSSID(st.Interface)
	return st
}

func currentClientMAC(cfg *Config) string {
	return interfaceMAC(wifiInterface(cfg))
}

func networkFingerprint() string {
	iface := detectedWiFiInterface()
	primary := defaultLocalInterface()
	return strings.Join([]string{
		iface, interfaceIPv4(iface), interfaceMAC(iface), primary,
	}, "|")
}

func wifiTargets(cfg *Config, st State) []string {
	if cfg == nil || cfg.WiFi == nil || !cfg.WiFi.Enabled {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		key := strings.ToLower(s)
		if s == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	for _, s := range cfg.WiFi.SSIDs {
		add(s)
	}
	add(st.LastSSID)
	return out
}

// runCommandTimeout prevents a wedged OS helper from stalling the daemon.
func runCommandTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("%s timed out after %s", name, timeout)
	}
	if err != nil {
		if text == "" {
			return text, fmt.Errorf("%s: %w", name, err)
		}
		return text, fmt.Errorf("%s: %w: %s", name, err, text)
	}
	return text, nil
}

func portalReachable(cfg *Config, st State) bool {
	seen := map[string]bool{}
	for _, raw := range []string{st.LastPortalURL, cfg.PortalURL} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		host := u.Host
		if u.Port() == "" {
			if u.Scheme == "https" {
				host = net.JoinHostPort(u.Hostname(), "443")
			} else {
				host = net.JoinHostPort(u.Hostname(), "80")
			}
		}
		if seen[host] {
			continue
		}
		seen[host] = true
		conn, err := net.DialTimeout("tcp", host, 1500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	for _, host := range cfg.PortalHosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		hostname := host
		if h, _, ok := strings.Cut(host, ":"); ok {
			hostname = h
		}
		for _, port := range []string{"8843", "443", "80"} {
			addr := net.JoinHostPort(hostname, port)
			if seen[addr] {
				continue
			}
			seen[addr] = true
			conn, err := net.DialTimeout("tcp", addr, 1200*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return true
			}
		}
	}
	return false
}

func (a *app) wifiEnabled() bool {
	return a.cfg.WiFi != nil && a.cfg.WiFi.Enabled
}

// ensureWiFi reconnects to a configured or previously successful SSID.
// allowSwitch permits trying another SSID while the current link is up; it is
// used after a login failure on the wrong network.
func (a *app) ensureWiFi(reason string, allowSwitch bool) bool {
	if !a.wifiEnabled() {
		return false
	}
	targets := wifiTargets(a.cfg, a.state.load())
	if len(targets) == 0 {
		return false
	}
	now := time.Now()
	if !a.lastWiFiAttempt.IsZero() && now.Sub(a.lastWiFiAttempt) < 10*time.Second {
		return false
	}

	st := currentWiFiStatus(a.cfg)
	if st.LinkUp {
		if !allowSwitch && !a.cfg.WiFi.Prefer {
			return false
		}
		// Never churn a working connection that can already reach the portal.
		if portalReachable(a.cfg, a.state.load()) {
			return false
		}
	}

	a.lastWiFiAttempt = now
	current := ""
	if st.LinkUp {
		current = wifiCurrentSSID(st.Interface)
	}
	if st.LinkUp && current != "" && sameSSID(current, targets[0]) && !allowSwitch && !a.cfg.WiFi.ResetOnStall {
		return false
	}

	a.log.Printf("wifi: %s; trying %s on %s", reason, strings.Join(targets, ", "), st.Interface)
	for _, ssid := range targets {
		if sameSSID(current, ssid) {
			continue
		}
		a.log.Printf("wifi: connecting to %q", ssid)
		if err := wifiConnectSSID(st.Interface, ssid); err != nil {
			a.log.Printf("wifi: %q failed: %v", ssid, err)
			continue
		}
		if a.waitForLink(30 * time.Second) {
			a.wifiFailures = 0
			a.state.clearFailures()
			a.log.Printf("wifi: connected to %q", ssid)
			return true
		}
		a.log.Printf("wifi: %q connected but no IPv4/default route yet", ssid)
	}

	if a.cfg.WiFi.ResetOnStall {
		a.wifiFailures++
		if a.wifiFailures >= 2 {
			a.log.Printf("wifi: power-cycling %s after repeated failures", st.Interface)
			err := wifiPowerCycle(st.Interface)
			if err != nil {
				a.log.Printf("wifi: power cycle failed: %v", err)
			} else if a.waitForLink(30 * time.Second) {
				a.wifiFailures = 0
				a.log.Printf("wifi: link restored after power cycle")
				return true
			}
		}
	}
	a.log.Printf("wifi: could not reconnect (tried %s)", strings.Join(targets, ", "))
	return false
}

func (a *app) waitForLink(timeout time.Duration) bool {
	iface := wifiInterface(a.cfg)
	if iface == "" {
		iface = defaultRoute().Interface
	}
	deadline := time.Now().Add(timeout)
	for {
		if interfaceIPv4(iface) != "" {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// maintainNetwork notices link changes even where netlink is unavailable and
// clears stale failure/portal state. It returns true when something changed,
// so the caller can run an immediate check.
func (a *app) maintainNetwork() bool {
	fp := networkFingerprint()
	changed := a.lastNetFP != "" && fp != a.lastNetFP
	if fp != a.lastNetFP {
		a.lastNetFP = fp
	}
	if changed {
		a.log.Printf("network changed: %s", fp)
		a.state.clearFailures()
		mac := currentClientMAC(a.cfg)
		if a.lastNetMAC != "" && mac != "" && !sameMAC(a.lastNetMAC, mac) {
			a.log.Printf("wifi: client MAC changed (%s -> %s); dropping cached portal URL", a.lastNetMAC, mac)
			a.state.clearPortalURL()
		}
		a.lastNetMAC = mac
	}
	// The MAC is also recorded on the first tick so a later change can be
	// detected even if no fingerprint "change" was observed first.
	if a.lastNetMAC == "" {
		a.lastNetMAC = currentClientMAC(a.cfg)
	}
	if !a.wifiEnabled() {
		return changed
	}
	st := currentWiFiStatus(a.cfg)
	if !st.LinkUp {
		// Do not wake the radio when a wired connection is already carrying
		// traffic; only recover Wi-Fi when it is the active path, there is no
		// path at all, or the user explicitly prefers a target network.
		if !a.cfg.WiFi.Prefer && st.Primary != "" && st.Primary != st.Interface {
			return changed
		}
		return a.ensureWiFi("link down", false) || changed
	}
	if a.cfg.WiFi.Prefer && !portalReachable(a.cfg, a.state.load()) {
		return a.ensureWiFi("preferred network", true) || changed
	}
	return changed
}

func isReachabilityError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, frag := range []string{
		"network is unreachable",
		"no route to host",
		"connection refused",
		"i/o timeout",
		"connection timed out",
		"context deadline exceeded",
		"dial tcp",
		"temporary failure in name resolution",
	} {
		if strings.Contains(s, frag) {
			return true
		}
	}
	return false
}

// scanSSIDs merges visible networks with OS-saved profiles. macOS redacts
// nearby SSIDs unless the calling app has Location permission, so the saved
// list remains useful there.
func scanSSIDs(iface string) (visible, saved []string) {
	visible = wifiVisibleSSIDs()
	saved = wifiKnownSSIDs(iface)
	sort.Strings(saved)
	return visible, saved
}
