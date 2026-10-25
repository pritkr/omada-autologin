//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func TestWindowsTaskXML(t *testing.T) {
	spec := serviceSpec{
		Executable:  `C:\Program Files\omada-autologin\omada-autologin.exe`,
		ConfigPath:  `C:\Users\A&B\AppData\Roaming\omada-autologin\config.json`,
		Interval:    60 * time.Second,
		Description: "Omada & captive-portal auto-login",
	}
	xml := windowsTaskXML(spec, `DOMAIN\student`)

	for _, want := range []string{
		"<LogonTrigger>",
		"<Enabled>true</Enabled>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<RestartOnFailure>",
		"<Count>999</Count>",
		"<Command>C:\\Program Files\\omada-autologin\\omada-autologin.exe</Command>",
		"run --config",
		"&amp;",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task XML is missing %q:\n%s", want, xml)
		}
	}
	if strings.Contains(xml, "<CalendarTrigger>") || strings.Contains(xml, "<Repetition>") {
		t.Error("task should be a logon daemon, not a repeating trigger")
	}
	if strings.Contains(xml, "--interval") {
		t.Error("interval should come from config, not from the task")
	}
}
