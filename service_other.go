//go:build !darwin && !linux && !windows

package main

import (
	"fmt"
	"runtime"
)

func installService(spec serviceSpec) error {
	return fmt.Errorf("automatic service install is not supported on %s; run `%s run` under your supervisor", runtime.GOOS, appName)
}

func uninstallService() error {
	return fmt.Errorf("automatic service install is not supported on %s", runtime.GOOS)
}
