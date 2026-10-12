// Package store provides a lightweight, file-backed key-value store for
// persisting device state across restarts. No external database required.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DeviceState is the persistent record kept per device.
type DeviceState struct {
	DeviceID    string            `json:"device_id"`
	LastSeen    time.Time         `json:"last_seen"`
	LastStatus  string            `json:"last_status"`
	LastMetrics map[string]any    `json:"last_metrics,omitempty"`
	AlertsSent  map[string]time.Time `json:"alerts_sent,omitempty"` // alert key → last sent
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Store is a simple JSON-file-backed state store.
//
// Updates only change memory and mark the store dirty; Run writes the file
// at most once per FlushInterval from a single goroutine, and Close writes
// it a final time. Writing the whole file on every update made the cost of
// each device poll grow with the number of devices — at 1,000 devices that
// was a full-file rewrite per poll from every polling goroutine at once.
type Store struct {
	path    string
	mu      sync.RWMutex
	devices map[string]*DeviceState
	dirty   bool

	flushMu sync.Mutex // one writer at a time
}

// FlushInterval is how often Run writes a dirty store to disk. The file
// only feeds restart-time state (last status, alert de-duplication), so a
// few seconds of lag after a crash is harmless.
const FlushInterval = 10 * time.Second

// New opens (or creates) the state file at path.
func New(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("creating store dir: %w", err)
	}

	s := &Store{
		path:    path,
		devices: map[string]*DeviceState{},
	}

	// Load existing state if present
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &s.devices)
	}

	return s, nil
}

// GetDevice returns the current persisted state for a device, or a fresh record.
func (s *Store) GetDevice(id string) *DeviceState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if d, ok := s.devices[id]; ok {
		return d
	}
	return &DeviceState{DeviceID: id, AlertsSent: map[string]time.Time{}}
}

// UpsertDevice writes updated state for a device and flushes to disk.
func (s *Store) UpsertDevice(state *DeviceState) error {
	state.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	if state.AlertsSent == nil {
		state.AlertsSent = map[string]time.Time{}
	}
	s.devices[state.DeviceID] = state
	s.dirty = true
	s.mu.Unlock()
	return nil
}

// MarkAlertSent records when an alert of a given key was last fired for a device.
func (s *Store) MarkAlertSent(deviceID, alertKey string) error {
	s.mu.Lock()
	d, ok := s.devices[deviceID]
	if !ok {
		d = &DeviceState{DeviceID: deviceID, AlertsSent: map[string]time.Time{}}
		s.devices[deviceID] = d
	}
	if d.AlertsSent == nil {
		d.AlertsSent = map[string]time.Time{}
	}
	d.AlertsSent[alertKey] = time.Now().UTC()
	d.UpdatedAt = time.Now().UTC()
	s.dirty = true
	s.mu.Unlock()
	return nil
}

// LastAlertSent returns when a given alert was last fired, or zero time if never.
func (s *Store) LastAlertSent(deviceID, alertKey string) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[deviceID]
	if !ok || d.AlertsSent == nil {
		return time.Time{}
	}
	return d.AlertsSent[alertKey]
}

// AllDevices returns a snapshot of all stored device states.
func (s *Store) AllDevices() []*DeviceState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*DeviceState, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	return out
}

// Run writes the store to disk every FlushInterval while it has changes,
// until ctx ends. Callers then call Close for the final write.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Flush(); err != nil {
				slog.Warn("state store write failed", "path", s.path, "error", err)
			}
		}
	}
}

// Close writes any pending changes. Safe to call more than once.
func (s *Store) Close() error {
	return s.Flush()
}

// Flush writes the store to disk if it has changed since the last write.
func (s *Store) Flush() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	b, err := json.Marshal(s.devices)
	if err == nil {
		s.dirty = false
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshalling store: %w", err)
	}
	if err := s.write(b); err != nil {
		s.mu.Lock()
		s.dirty = true // retry on the next tick
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Store) write(b []byte) error {
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return fmt.Errorf("writing store tmp: %w", err)
	}
	return os.Rename(tmp, s.path)
}
