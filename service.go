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
	out, err := runCommandOutput(name, args...)
	if err != nil {
		if out == "" {
			return fmt.Errorf("%s: %w", name, err)
		}
		return fmt.Errorf("%s: %w: %s", name, err, out)
	}
	return nil
}

func runCommandOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
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
