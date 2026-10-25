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
	timerLabel   = serviceLabel + ".timer"
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
		"once",
		"--config",
		systemdQuote(spec.ConfigPath),
	}, " ")

	service := fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target

[Service]
Type=oneshot
ExecStart=%s
Nice=10
`, spec.Description, execStart)

	interval := int(spec.Interval.Seconds())
	if interval < 5 {
		interval = 5
	}
	timer := fmt.Sprintf(`[Unit]
Description=%s (timer)

[Timer]
OnBootSec=%ds
OnUnitActiveSec=%ds
AccuracySec=1s
Persistent=true

[Install]
WantedBy=timers.target
`, spec.Description, interval, interval)

	servicePath := filepath.Join(unitDir, serviceLabel+".service")
	timerPath := filepath.Join(unitDir, timerLabel)
	if err := os.WriteFile(servicePath, []byte(service), 0o600); err != nil {
		return fmt.Errorf("write systemd service: %w", err)
	}
	if err := os.WriteFile(timerPath, []byte(timer), 0o600); err != nil {
		return fmt.Errorf("write systemd timer: %w", err)
	}

	if err := runCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemd user session is not available: %w", err)
	}
	if err := runCommand("systemctl", "--user", "enable", "--now", timerLabel); err != nil {
		return fmt.Errorf("enable systemd timer: %w", err)
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
