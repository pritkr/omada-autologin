//go:build windows

package main

import (
	"fmt"
)

const taskName = "Omada Autologin"

func installService(spec serviceSpec) error {
	// Task Scheduler's minimum repeating interval is one minute.
	action := fmt.Sprintf(`"%s" once --config "%s"`, spec.Executable, spec.ConfigPath)
	if err := runCommand("schtasks", "/Create", "/F",
		"/TN", taskName,
		"/SC", "MINUTE", "/MO", "1",
		"/TR", action,
	); err != nil {
		return fmt.Errorf("create scheduled task: %w", err)
	}
	// Run immediately so the user does not wait a full minute.
	_ = runCommand("schtasks", "/Run", "/TN", taskName)
	return nil
}

func uninstallService() error {
	if err := runCommand("schtasks", "/Delete", "/F", "/TN", taskName); err != nil {
		return fmt.Errorf("delete scheduled task: %w", err)
	}
	return nil
}
