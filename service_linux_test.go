//go:build linux

package main

import (
	"strings"
	"testing"
	"time"
)

func TestLinuxServiceUnit(t *testing.T) {
	spec := serviceSpec{
		Executable:  "/home/me/.local/bin/omada-autologin",
		ConfigPath:  "/home/me/.config/omada-autologin/config.json",
		Interval:    60 * time.Second,
		Description: "Omada captive-portal auto-login",
	}
	execStart := "/home/me/.local/bin/omada-autologin run --config /home/me/.config/omada-autologin/config.json"
	unit := linuxServiceUnit(spec, execStart)

	for _, want := range []string{
		"Description=Omada captive-portal auto-login",
		"Type=simple",
		"ExecStart=" + execStart,
		"ExecReload=/bin/kill -HUP $MAINPID",
		"Restart=always",
		"RestartSec=5",
		"StartLimitIntervalSec=0",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit is missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "OnUnitActiveSec") || strings.Contains(unit, ".timer") {
		t.Errorf("unit still uses a timer:\n%s", unit)
	}
	if strings.Contains(unit, "--interval") {
		t.Error("interval should come from config, not from the unit")
	}
}

func TestLinuxServiceExecStartQuoting(t *testing.T) {
	// This mirrors the arguments installService builds, with a path that
	// needs systemd quoting.
	execStart := systemdQuote("/home/a b/omada") + " run --config " + systemdQuote("/home/a b/config.json")
	if !strings.HasPrefix(execStart, `"/home/a b/omada" run --config "/home/a b/config.json"`) {
		t.Fatalf("exec start = %q", execStart)
	}
}
