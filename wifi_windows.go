//go:build windows

package main

import (
	"fmt"
	"strings"
	"time"
)

func defaultRoute() routeInfo {
	out, err := runCommandTimeout(5*time.Second, "route", "print", "-4", "0.0.0.0")
	if err != nil {
		return routeInfo{}
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == "0.0.0.0" && fields[1] == "0.0.0.0" {
			return routeInfo{
				Interface: interfaceNameByIPv4(fields[3]),
				Gateway:   fields[2],
			}
		}
	}
	return routeInfo{}
}

func wifiDefaultInterface() string {
	out, err := runCommandTimeout(5*time.Second, "netsh", "wlan", "show", "interfaces")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "Name" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func wifiCurrentSSID(iface string) string {
	out, err := runCommandTimeout(5*time.Second, "netsh", "wlan", "show", "interfaces")
	if err != nil {
		return ""
	}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Name":
			current = strings.TrimSpace(v)
		case "SSID":
			if iface == "" || strings.EqualFold(current, iface) {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

func wifiKnownSSIDs(iface string) []string {
	out, err := runCommandTimeout(10*time.Second, "netsh", "wlan", "show", "profiles")
	if err != nil {
		return nil
	}
	var ssids []string
	for _, line := range strings.Split(out, "\n") {
		_, v, ok := strings.Cut(line, ":")
		if ok && strings.Contains(line, "All User Profile") {
			if name := strings.TrimSpace(v); name != "" {
				ssids = append(ssids, name)
			}
		}
	}
	return ssids
}

func wifiVisibleSSIDs() []string {
	out, err := runCommandTimeout(15*time.Second, "netsh", "wlan", "show", "networks")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var ssids []string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "SSID ") {
			continue
		}
		if _, v, ok := strings.Cut(trimmed, ":"); ok {
			name := strings.TrimSpace(v)
			if name != "" && !seen[name] {
				seen[name] = true
				ssids = append(ssids, name)
			}
		}
	}
	return ssids
}

func wifiConnectSSID(iface, ssid string) error {
	args := []string{"wlan", "connect", "name=" + ssid}
	if iface != "" {
		args = append(args, "interface="+iface)
	}
	out, err := runCommandTimeout(35*time.Second, "netsh", args...)
	if err != nil {
		if out != "" {
			return fmt.Errorf("%s", out)
		}
		return err
	}
	return nil
}

func wifiPowerCycle(iface string) error {
	return fmt.Errorf("Wi-Fi power cycling is not supported on Windows")
}
