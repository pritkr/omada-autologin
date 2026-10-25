//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	serviceLabel = "omada-autologin"
	timerLabel   = serviceLabel + ".timer" // removed from older installs
)

func installService(spec serviceSpec) error {
	if err := requireNonRoot(); err != nil {
		return err
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(base, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		return err
	}

	execStart := strings.Join([]string{
		systemdQuote(spec.Executable),
		"run",
		"--config",
		systemdQuote(spec.ConfigPath),
	}, " ")

	service := linuxServiceUnit(spec, execStart)
	servicePath := filepath.Join(unitDir, serviceLabel+".service")
	// Migrate away from the timer-based installer used by v0.1.x.
	_ = runCommand("systemctl", "--user", "disable", "--now", timerLabel)
	_ = os.Remove(filepath.Join(unitDir, timerLabel))
	if err := os.WriteFile(servicePath, []byte(service), 0o600); err != nil {
		return fmt.Errorf("write systemd service: %w", err)
	}

	if err := runCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemd user session is not available (Hyprland needs a systemd user session, or use `omada-autologin run`): %w", err)
	}
	if err := runCommand("systemctl", "--user", "enable", "--now", serviceLabel+".service"); err != nil {
		return fmt.Errorf("enable systemd service: %w", err)
	}
	return nil
}

func uninstallService() error {
	if err := requireNonRoot(); err != nil {
		return err
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(base, "systemd", "user")
	_ = runCommand("systemctl", "--user", "disable", "--now", serviceLabel+".service")
	_ = runCommand("systemctl", "--user", "disable", "--now", timerLabel)
	for _, name := range []string{serviceLabel + ".service", timerLabel} {
		path := filepath.Join(unitDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	_ = runCommand("systemctl", "--user", "daemon-reload")
	return nil
}

func linuxServiceUnit(spec serviceSpec, execStart string) string {
	return fmt.Sprintf(`[Unit]
Description=%s
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=%s
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5
TimeoutStopSec=15
Nice=10
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=default.target
`, spec.Description, execStart)
}
