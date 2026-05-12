// Package store provides a lightweight, file-backed key-value store for
// persisting device state across restarts. No external database required.
package store

import (
	"encoding/json"
	"fmt"
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
type Store struct {
	path    string
	mu      sync.RWMutex
	devices map[string]*DeviceState
}

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
	snapshot := s.snapshot()
	s.mu.Unlock()
	return s.flush(snapshot)
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
	snapshot := s.snapshot()
	s.mu.Unlock()
	return s.flush(snapshot)
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

func (s *Store) snapshot() map[string]*DeviceState {
	cp := make(map[string]*DeviceState, len(s.devices))
	for k, v := range s.devices {
		cp[k] = v
	}
	return cp
}

func (s *Store) flush(data map[string]*DeviceState) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling store: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return fmt.Errorf("writing store tmp: %w", err)
	}
	return os.Rename(tmp, s.path)
}
