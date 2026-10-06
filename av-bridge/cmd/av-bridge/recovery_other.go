//go:build !windows

package main

// ensureServiceRecovery is a no-op off Windows: the systemd unit's
// Restart=on-failure already restarts the process after an update exit.
func ensureServiceRecovery() {}
