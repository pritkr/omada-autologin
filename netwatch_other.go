//go:build !linux

package main

// watchNetwork is a no-op outside Linux; the daemon falls back to its normal
// health-check interval.
func watchNetwork(chan<- struct{}) func() {
	return func() {}
}
