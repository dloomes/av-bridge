//go:build windows

package main

import (
	"log/slog"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

// ensureServiceRecovery sets the av-bridge Windows service to restart
// when the process exits unexpectedly. The self-updater relies on this:
// it exits after swapping the binary and the Service Control Manager
// starts the new one. Done on every start (it's idempotent) so services
// installed before this existed are fixed without reinstalling.
func ensureServiceRecovery() {
	m, err := mgr.Connect()
	if err != nil {
		slog.Warn("service recovery: connect to service manager", "error", err)
		return
	}
	defer m.Disconnect()
	s, err := m.OpenService("av-bridge")
	if err != nil {
		slog.Warn("service recovery: open service", "error", err)
		return
	}
	defer s.Close()
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	// Reset the failure count after a day of clean running.
	if err := s.SetRecoveryActions(actions, 24*60*60); err != nil {
		slog.Warn("service recovery: set actions", "error", err)
	}
}
