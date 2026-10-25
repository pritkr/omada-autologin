//go:build darwin

package main

import (
	"fmt"
	"strings"
	"time"
)

func defaultRoute() routeInfo {
	out, err := runCommandTimeout(3*time.Second, "route", "-n", "get", "default")
	if err != nil {
		return routeInfo{}
	}
	var r routeInfo
	for _, line := range strings.Split(out, "\n") {
		if _, v, ok := strings.Cut(line, "interface:"); ok {
			r.Interface = strings.TrimSpace(v)
		}
		if _, v, ok := strings.Cut(line, "gateway:"); ok {
			r.Gateway = strings.TrimSpace(v)
		}
	}
	return r
}

func wifiDefaultInterface() string {
	out, err := runCommandTimeout(3*time.Second, "networksetup", "-listallhardwareports")
	if err != nil {
		return ""
	}
	inWiFi := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Hardware Port:") {
			inWiFi = strings.Contains(strings.ToLower(line), "wi-fi")
			continue
		}
		if inWiFi && strings.HasPrefix(line, "Device:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Device:"))
		}
	}
	return ""
}

func wifiCurrentSSID(iface string) string {
	if iface == "" {
		iface = wifiDefaultInterface()
	}
	if iface == "" {
		return ""
	}
	out, err := runCommandTimeout(10*time.Second, "networksetup", "-getairportnetwork", iface)
	if err == nil {
		if _, v, ok := strings.Cut(out, ":"); ok {
			v = strings.TrimSpace(v)
			if v != "" && !strings.Contains(strings.ToLower(out), "not associated") {
				return v
			}
		}
	}
	// Fallback for macOS versions where networksetup no longer reports the
	// association. SSID is redacted without Location permission, so treat
	// "<redacted>" as unknown rather than as a network name.
	out, err = runCommandTimeout(10*time.Second, "ipconfig", "getsummary", iface)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "SSID" {
			v = strings.TrimSpace(v)
			if v != "" && v != "<redacted>" {
				return v
			}
		}
	}
	return ""
}

func wifiKnownSSIDs(iface string) []string {
	if iface == "" {
		iface = wifiDefaultInterface()
	}
	if iface == "" {
		return nil
	}
	out, err := runCommandTimeout(10*time.Second, "networksetup", "-listpreferredwirelessnetworks", iface)
	if err != nil {
		return nil
	}
	var outSSIDs []string
	for i, line := range strings.Split(out, "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		outSSIDs = append(outSSIDs, strings.TrimSpace(line))
	}
	return outSSIDs
}

func wifiVisibleSSIDs() []string {
	// system_profiler is the only unprivileged scan source left on current
	// macOS. Names may be redacted to "<redacted>"; those rows are skipped.
	out, err := runCommandTimeout(20*time.Second, "system_profiler", "SPAirPortDataType")
	if err != nil {
		return nil
	}
	inOther := false
	sectionIndent := 0
	seen := map[string]bool{}
	var ssids []string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Other Local Wi-Fi Networks:") {
			inOther = true
			sectionIndent = len(line) - len(strings.TrimLeft(line, " "))
			continue
		}
		if !inOther || trimmed == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent != sectionIndent+2 || !strings.HasSuffix(trimmed, ":") {
			continue
		}
		name := strings.TrimSpace(strings.TrimSuffix(trimmed, ":"))
		if name == "" || name == "<redacted>" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		ssids = append(ssids, name)
	}
	return ssids
}

func wifiConnectSSID(iface, ssid string) error {
	if iface == "" {
		iface = wifiDefaultInterface()
	}
	if iface == "" {
		return fmt.Errorf("no Wi-Fi interface found")
	}
	out, err := runCommandTimeout(35*time.Second, "networksetup", "-setairportnetwork", iface, ssid)
	if err != nil {
		if out != "" {
			return fmt.Errorf("%s", out)
		}
		return err
	}
	return nil
}

func wifiPowerCycle(iface string) error {
	if iface == "" {
		iface = wifiDefaultInterface()
	}
	if iface == "" {
		return fmt.Errorf("no Wi-Fi interface found")
	}
	if _, err := runCommandTimeout(10*time.Second, "networksetup", "-setairportpower", iface, "off"); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	_, err := runCommandTimeout(10*time.Second, "networksetup", "-setairportpower", iface, "on")
	return err
}
