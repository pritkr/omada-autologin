//go:build linux

package main

import (
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// watchNetwork listens for link and address changes on Linux (NetworkManager,
// systemd-networkd, iwd, and plain `ip link` all publish these). It gives the
// daemon an immediate trigger when Wi-Fi reassociates, which is what makes the
// CachyOS/Hyprland setup feel instant without polling every few seconds.
func watchNetwork(wake chan<- struct{}) func() {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return func() {}
	}
	groups := uint32(unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		_ = unix.Close(fd)
		return func() {}
	}
	// A receive timeout lets the goroutine notice shutdown without relying
	// on close() interrupting a blocked recv.
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1})

	done := make(chan struct{})
	var once sync.Once
	go func() {
		buf := make([]byte, 8192)
		var last time.Time
		for {
			select {
			case <-done:
				return
			default:
			}
			n, _, err := unix.Recvfrom(fd, buf, 0)
			if err != nil {
				if err == unix.EINTR || err == unix.EAGAIN || err == unix.EWOULDBLOCK {
					continue
				}
				return
			}
			if n <= 0 {
				continue
			}
			// Coalesce the burst of messages a single association produces.
			if time.Since(last) < 500*time.Millisecond {
				continue
			}
			last = time.Now()
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(done)
			_ = unix.Close(fd)
		})
	}
}
