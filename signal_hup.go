//go:build !js

package main

import (
	"os"
	"syscall"
)

// reloadSignal is SIGHUP on every platform that has it. A reload is a
// convenience; on js/wasm there is no service manager to send it.
func reloadSignal() os.Signal { return syscall.SIGHUP }
