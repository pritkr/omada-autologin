//go:build darwin || windows

package main

import (
	"sync"
	"time"
)

// watchNetwork polls the cheap network fingerprint (wireless interface, IPv4,
// MAC, primary interface) so macOS and Windows get an immediate check after a
// Wi-Fi reassociation or DHCP change instead of waiting for the full interval.
func watchNetwork(wake chan<- struct{}) func() {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		last := networkFingerprint()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				cur := networkFingerprint()
				if cur == last {
					continue
				}
				last = cur
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}
