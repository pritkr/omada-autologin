//go:build !darwin && !linux && !windows

package main

import "fmt"

func defaultRoute() routeInfo { return routeInfo{} }

func wifiDefaultInterface() string { return "" }

func wifiCurrentSSID(string) string { return "" }

func wifiKnownSSIDs(string) []string { return nil }

func wifiVisibleSSIDs() []string { return nil }

func wifiConnectSSID(string, string) error {
	return fmt.Errorf("Wi-Fi reconnect is not supported on this OS")
}

func wifiPowerCycle(string) error {
	return fmt.Errorf("Wi-Fi power cycling is not supported on this OS")
}
