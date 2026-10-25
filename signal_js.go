//go:build js

package main

import "os"

// reloadSignal is unavailable in the browser runtime.
func reloadSignal() os.Signal { return nil }
