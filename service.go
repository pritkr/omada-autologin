package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type serviceSpec struct {
	Executable  string
	ConfigPath  string
	Interval    time.Duration
	LogPath     string
	DefaultLog  string
	Description string
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("%s: %w", name, err)
		}
		return fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return nil
}

func requireNonRoot() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("run this command as your user, not with sudo/root")
	}
	return nil
}

// systemdQuote quotes one ExecStart argument. systemd uses shell-like double
// quotes, expands $VAR and %specifiers, and treats $$ / %% as literals.
func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\$%") {
		return s
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "$", "$$")
	s = strings.ReplaceAll(s, "%", "%%")
	return `"` + s + `"`
}
