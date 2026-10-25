//go:build darwin

package main

import (
	"strings"
	"testing"
	"time"
)

func TestDarwinPlist(t *testing.T) {
	spec := serviceSpec{
		Executable:  "/Users/me/.local/bin/omada-autologin",
		ConfigPath:  "/Users/me/Library/Application Support/omada-autologin/config.json",
		Interval:    60 * time.Second,
		LogPath:     "/Users/me/Library/Caches/omada-autologin/service.log",
		Description: "Omada captive-portal auto-login",
	}
	plist := darwinPlist(spec)

	for _, want := range []string{
		"<string>com.omada.autologin</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>SuccessfulExit</key>",
		"<key>ThrottleInterval</key>",
		"<string>run</string>",
		"<string>--config</string>",
		"<key>ProcessType</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist is missing %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "<key>StartInterval</key>") {
		t.Error("plist still uses StartInterval")
	}
	if strings.Contains(plist, "--interval") {
		t.Error("interval should come from config, not from the plist")
	}
	if !strings.Contains(plist, "<string>/Users/me/Library/Application Support/omada-autologin/config.json</string>") {
		t.Error("config path with spaces should stay one plist string")
	}
}

func TestDarwinPlistEscapesAmpersand(t *testing.T) {
	spec := serviceSpec{
		Executable: "/tmp/omada-autologin",
		ConfigPath: "/tmp/a&b/config.json",
		LogPath:    "/tmp/a&b/service.log",
	}
	plist := darwinPlist(spec)
	if !strings.Contains(plist, "/tmp/a&amp;b/config.json") {
		t.Errorf("ampersand was not escaped:\n%s", plist)
	}
}
