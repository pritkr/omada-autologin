//go:build !linux && !darwin && !windows

package main

// watchNetwork is a no-op on platforms without a link watcher; the daemon
// falls back to its normal health-check interval.
func watchNetwork(chan<- struct{}) func() {
	return func() {}
}
