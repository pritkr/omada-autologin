//go:build darwin

package main

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
)

const serviceLabel = "com.omada.autologin"

func installService(spec serviceSpec) error {
	if err := requireNonRoot(); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	plistPath := filepath.Join(dir, serviceLabel+".plist")
	plist := darwinPlist(spec)

	if err := os.WriteFile(plistPath, []byte(plist), 0o600); err != nil {
		return fmt.Errorf("write LaunchAgent: %w", err)
	}

	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = runCommand("launchctl", "bootout", domain+"/"+serviceLabel)
	if err := runCommand("launchctl", "bootstrap", domain, plistPath); err != nil {
		return fmt.Errorf("load LaunchAgent: %w", err)
	}
	_ = runCommand("launchctl", "enable", domain+"/"+serviceLabel)
	_ = runCommand("launchctl", "kickstart", "-k", domain+"/"+serviceLabel)
	return nil
}

func darwinPlist(spec serviceSpec) string {
	args := []string{
		spec.Executable,
		"run",
		"--config", spec.ConfigPath,
	}
	argXML := ""
	for _, a := range args {
		argXML += "        <string>" + html.EscapeString(a) + "</string>\n"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
%s    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ThrottleInterval</key>
    <integer>10</integer>
    <key>ProcessType</key>
    <string>Background</string>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, serviceLabel, argXML, html.EscapeString(spec.LogPath), html.EscapeString(spec.LogPath))
}

func uninstallService() error {
	if err := requireNonRoot(); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = runCommand("launchctl", "bootout", domain+"/"+serviceLabel)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove LaunchAgent: %w", err)
	}
	return nil
}
