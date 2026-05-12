package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dloomes/av-bridge/internal/config"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestLoad_ValidMinimal(t *testing.T) {
	path := writeConfig(t, `
cloud:
  webhook_url: "https://example.com/ingest"
devices:
  - id: display-01
    name: Test Display
    type: display
    protocol: rest
    address: "192.168.1.1"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cloud.WebhookURL != "https://example.com/ingest" {
		t.Errorf("unexpected webhook URL: %q", cfg.Cloud.WebhookURL)
	}
	if len(cfg.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(cfg.Devices))
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	path := writeConfig(t, `
cloud:
  webhook_url: "https://example.com/ingest"
devices:
  - id: cam-01
    name: Camera
    type: camera
    protocol: serial
    address: /dev/ttyUSB0
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hub.ListenAddr != "0.0.0.0:8080" {
		t.Errorf("expected default listen addr, got %q", cfg.Hub.ListenAddr)
	}
	if cfg.Cloud.PushInterval != 30*time.Second {
		t.Errorf("expected 30s push interval, got %v", cfg.Cloud.PushInterval)
	}
	if cfg.Devices[0].BaudRate != 9600 {
		t.Errorf("expected default baud rate 9600, got %d", cfg.Devices[0].BaudRate)
	}
	if cfg.Devices[0].PollRate != 60*time.Second {
		t.Errorf("expected default poll rate 60s, got %v", cfg.Devices[0].PollRate)
	}
}

func TestLoad_MissingWebhookURL(t *testing.T) {
	path := writeConfig(t, `
cloud:
  api_key: "key"
devices:
  - id: d1
    name: D1
    type: display
    protocol: rest
    address: "1.2.3.4"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for missing webhook_url")
	}
}

func TestLoad_DuplicateDeviceID(t *testing.T) {
	path := writeConfig(t, `
cloud:
  webhook_url: "https://example.com/ingest"
devices:
  - id: same-id
    name: A
    type: display
    protocol: rest
    address: "1.1.1.1"
  - id: same-id
    name: B
    type: audio
    protocol: telnet
    address: "2.2.2.2:23"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate device ID")
	}
}

func TestLoad_InvalidProtocol(t *testing.T) {
	path := writeConfig(t, `
cloud:
  webhook_url: "https://example.com/ingest"
devices:
  - id: d1
    name: D1
    type: display
    protocol: mqtt
    address: "1.2.3.4"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unsupported protocol")
	}
}

func TestLoad_InvalidDeviceType(t *testing.T) {
	path := writeConfig(t, `
cloud:
  webhook_url: "https://example.com/ingest"
devices:
  - id: d1
    name: D1
    type: projector
    protocol: rest
    address: "1.2.3.4"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unsupported device type")
	}
}

func TestLoad_EnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_WEBHOOK", "https://env-expanded.example.com/hook")
	path := writeConfig(t, `
cloud:
  webhook_url: "${TEST_WEBHOOK}"
devices:
  - id: d1
    name: D
    type: display
    protocol: rest
    address: "1.2.3.4"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cloud.WebhookURL != "https://env-expanded.example.com/hook" {
		t.Errorf("env var not expanded: got %q", cfg.Cloud.WebhookURL)
	}
}

func TestLoad_TLSValidation(t *testing.T) {
	path := writeConfig(t, `
cloud:
  webhook_url: "https://example.com/ingest"
api:
  tls:
    enabled: true
devices:
  - id: d1
    name: D
    type: display
    protocol: rest
    address: "1.2.3.4"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error when TLS enabled without cert/key")
	}
}
