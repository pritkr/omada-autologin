//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func defaultRoute() routeInfo {
	out, err := runCommandTimeout(3*time.Second, "ip", "-4", "route", "show", "default")
	if err == nil {
		var r routeInfo
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			for i := 0; i < len(fields); i++ {
				switch fields[i] {
				case "via":
					if i+1 < len(fields) {
						r.Gateway = fields[i+1]
					}
				case "dev":
					if i+1 < len(fields) {
						r.Interface = fields[i+1]
					}
				}
			}
			if r.Interface != "" {
				return r
			}
		}
		if r.Gateway != "" {
			return r
		}
	}
	return procDefaultRoute()
}

// procDefaultRoute is the fallback for minimal systems where `ip` is absent.
// /proc/net/route stores the gateway as a little-endian hex IPv4 address.
func procDefaultRoute() routeInfo {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return routeInfo{}
	}
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		gw := hexLEToIPv4(fields[2])
		if gw == "" {
			continue
		}
		return routeInfo{Interface: fields[0], Gateway: gw}
	}
	return routeInfo{}
}

func hexLEToIPv4(s string) string {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func wifiDefaultInterface() string {
	// Find the wireless device, not merely the default route: a docked laptop
	// may route through Ethernet while the portal still uses the Wi-Fi MAC.
	out, err := runCommandTimeout(3*time.Second, "nmcli", "-t", "-f", "DEVICE,TYPE", "device", "status")
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			dev, typ, ok := strings.Cut(line, ":")
			if ok && typ == "wifi" {
				return dev
			}
		}
	}
	entries, err := os.ReadDir("/sys/class/net")
	if err == nil {
		for _, entry := range entries {
			if _, err := os.Stat(filepath.Join("/sys/class/net", entry.Name(), "wireless")); err == nil {
				return entry.Name()
			}
		}
	}
	return ""
}

func wifiCurrentSSID(iface string) string {
	if iface == "" {
		iface = wifiDefaultInterface()
	}
	out, err := runCommandTimeout(5*time.Second, "nmcli", "-t", "-f", "GENERAL.CONNECTION", "device", "show", iface)
	if err == nil {
		if _, v, ok := strings.Cut(out, ":"); ok {
			v = strings.TrimSpace(v)
			if v != "" && v != "--" {
				return v
			}
		}
	}
	out, err = runCommandTimeout(5*time.Second, "iwgetid", "-r", iface)
	if err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	// iwd has no nmcli equivalent; its station table is the remaining source.
	out, err = runCommandTimeout(5*time.Second, "iwctl", "station", iface, "show")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "Connected network" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func wifiKnownSSIDs(iface string) []string {
	out, err := runCommandTimeout(5*time.Second, "nmcli", "-t", "-f", "NAME,TYPE", "connection", "show")
	if err != nil {
		return nil
	}
	var ssids []string
	for _, line := range strings.Split(out, "\n") {
		name, typ, ok := strings.Cut(line, ":")
		if ok && typ == "802-11-wireless" {
			ssids = append(ssids, name)
		}
	}
	return ssids
}

func wifiVisibleSSIDs() []string {
	out, err := runCommandTimeout(15*time.Second, "nmcli", "-t", "-f", "SSID", "device", "wifi", "list")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var ssids []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || name == "--" || name == "SSID" || seen[name] {
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
	var errs []string
	if _, err := exec.LookPath("nmcli"); err == nil {
		args := []string{"--wait", "30"}
		if iface != "" {
			args = append(args, "device", "wifi", "connect", ssid, "ifname", iface)
		} else {
			args = append(args, "device", "wifi", "connect", ssid)
		}
		out, err := runCommandTimeout(35*time.Second, "nmcli", args...)
		if err == nil {
			return nil
		}
		// A saved profile may connect even when a fresh scan is unavailable.
		if out2, err2 := runCommandTimeout(35*time.Second, "nmcli", "--wait", "30", "connection", "up", "id", ssid); err2 == nil {
			return nil
		} else if out2 != "" {
			errs = append(errs, out2)
		} else if out != "" {
			errs = append(errs, out)
		} else {
			errs = append(errs, err.Error())
		}
	}
	if _, err := exec.LookPath("iwctl"); err == nil && iface != "" {
		if out, err := runCommandTimeout(35*time.Second, "iwctl", "station", iface, "connect", ssid); err == nil {
			return nil
		} else if out != "" {
			errs = append(errs, out)
		} else {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return fmt.Errorf("no supported Wi-Fi manager found (need nmcli or iwctl)")
}

func wifiPowerCycle(iface string) error {
	if _, err := exec.LookPath("nmcli"); err != nil {
		return fmt.Errorf("power cycling needs nmcli")
	}
	if _, err := runCommandTimeout(10*time.Second, "nmcli", "radio", "wifi", "off"); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	_, err := runCommandTimeout(10*time.Second, "nmcli", "radio", "wifi", "on")
	return err
}
