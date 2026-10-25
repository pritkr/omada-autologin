//go:build windows

package main

import (
	"fmt"
	"html"
	"os"
	"os/user"
)

const taskName = "Omada Autologin"

func installService(spec serviceSpec) error {
	userID, err := currentWindowsUser()
	if err != nil {
		return err
	}
	xml := windowsTaskXML(spec, userID)

	f, err := os.CreateTemp("", "omada-autologin-*.xml")
	if err != nil {
		return fmt.Errorf("create task XML: %w", err)
	}
	xmlPath := f.Name()
	defer os.Remove(xmlPath)
	if _, err := f.WriteString(xml); err != nil {
		_ = f.Close()
		return fmt.Errorf("write task XML: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write task XML: %w", err)
	}

	if err := runCommand("schtasks", "/Create", "/F", "/TN", taskName, "/XML", xmlPath); err != nil {
		// Some older Task Scheduler versions reject the XML schema. Fall
		// back to a logon task; it still starts the resilient daemon.
		action := fmt.Sprintf(`"%s" run --config "%s"`, spec.Executable, spec.ConfigPath)
		if err2 := runCommand("schtasks", "/Create", "/F", "/TN", taskName, "/SC", "ONLOGON", "/TR", action); err2 != nil {
			return fmt.Errorf("create scheduled task: %w (XML fallback: %v)", err, err2)
		}
	}
	_ = runCommand("schtasks", "/Run", "/TN", taskName)
	return nil
}

func windowsTaskXML(spec serviceSpec, userID string) string {
	args := fmt.Sprintf(`run --config "%s"`, spec.ConfigPath)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>%s</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%s</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>
`, html.EscapeString(spec.Description), html.EscapeString(userID), html.EscapeString(userID),
		html.EscapeString(spec.Executable), html.EscapeString(args))
}

func currentWindowsUser() (string, error) {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username, nil
	}
	name := os.Getenv("USERNAME")
	if name == "" {
		return "", fmt.Errorf("cannot determine the current Windows user")
	}
	if domain := os.Getenv("USERDOMAIN"); domain != "" {
		name = domain + `\` + name
	}
	return name, nil
}

func uninstallService() error {
	if err := runCommand("schtasks", "/Delete", "/F", "/TN", taskName); err != nil {
		return fmt.Errorf("delete scheduled task: %w", err)
	}
	return nil
}
