package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Hub     HubConfig      `yaml:"hub"`
	Cloud   CloudConfig    `yaml:"cloud"`
	Lens    LensConfig     `yaml:"lens"`
	API     APIConfig      `yaml:"api"`
	Alerts  AlertsConfig   `yaml:"alerts"`
	Devices []DeviceConfig `yaml:"devices"`
}

// LensConfig configures the optional Poly Lens enrichment layer. When enabled,
// devices tagged with lens_managed: "true" are looked up in Lens by serial
// number to fill in fields the device's local REST API doesn't expose
// (notably MAC address, asset tag, room/site assignment, last-seen).
type LensConfig struct {
	Enabled      bool          `yaml:"enabled"`
	ClientID     string        `yaml:"client_id"`
	ClientSecret string        `yaml:"client_secret"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

type HubConfig struct {
	ListenAddr         string        `yaml:"listen_addr"`
	HeartbeatPeriod    time.Duration `yaml:"heartbeat_period"`
	LogLevel           string        `yaml:"log_level"`
	StorePath          string        `yaml:"store_path"` // path for persisted device state
	CollectorID        string        `yaml:"collector_id"`
	SiteID             string        `yaml:"site_id"`
	DeviceSyncInterval time.Duration `yaml:"device_sync_interval"`
}

type CloudConfig struct {
	WebhookURL    string        `yaml:"webhook_url"`
	APIKey        string        `yaml:"api_key"`
	PushInterval  time.Duration `yaml:"push_interval"`
	RetryAttempts int           `yaml:"retry_attempts"`
	RetryDelay    time.Duration `yaml:"retry_delay"`
	TLSSkipVerify bool          `yaml:"tls_skip_verify"`
	// PortalAPI is the cloud service's base URL (e.g. http://cloud-host:8090).
	// Used by the command poller for /bridge/poll and /bridge/commands/{id}/result.
	// When empty, the command poller is disabled.
	PortalAPI string `yaml:"portal_api"`
	// HMACSecret signs outbound calls to the cloud (ingest push, command poll,
	// command result). Must match the secret stored for this collector in the
	// cloud DB. When empty, outbound HMAC signing is skipped and the command
	// poller is disabled.
	HMACSecret string `yaml:"hmac_secret"`
	// CommandPollInterval is how often the bridge polls the cloud for pending
	// commands. Defaults to 5s.
	CommandPollInterval time.Duration `yaml:"command_poll_interval"`
	// CommandMaxBatch caps how many commands the bridge claims per poll.
	// Defaults to 10.
	CommandMaxBatch int `yaml:"command_max_batch"`
}

type APIConfig struct {
	Auth AuthConfig `yaml:"auth"`
	TLS  TLSConfig  `yaml:"tls"`
}

type AuthConfig struct {
	Enabled    bool     `yaml:"enabled"`
	APIKeys    []string `yaml:"api_keys"`
	HMACSecret string   `yaml:"hmac_secret"`
}

type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// AlertsConfig defines thresholds for automatic alerting
type AlertsConfig struct {
	OfflineAfter   time.Duration `yaml:"offline_after"`   // default 5m
	DegradedAfter  time.Duration `yaml:"degraded_after"`  // default 2m
	RepeatInterval time.Duration `yaml:"repeat_interval"` // default 30m
}

type DeviceConfig struct {
	ID            string             `yaml:"id"`
	Name          string             `yaml:"name"`
	Location      string             `yaml:"location"`
	Type          string             `yaml:"type"`
	Protocol      string             `yaml:"protocol"`
	Address       string             `yaml:"address"`
	BaudRate      int                `yaml:"baud_rate"`
	Username      string             `yaml:"username"`
	Password      string             `yaml:"password"`
	PollRate      time.Duration      `yaml:"poll_rate"`
	Commands      map[string]string  `yaml:"commands"`
	Tags          map[string]string  `yaml:"tags"`
	Subscriptions []SubscriptionSpec `yaml:"subscriptions"`
}

// SubscriptionSpec describes a single push-notification subscription that an
// adapter should register on connect. Currently consumed by the Tesira
// adapter; other subscription-capable adapters can adopt the same shape.
//
// For Tesira TTP this maps directly to:
//
//	<tag> subscribe <attribute> <channel> <label> <rate>
type SubscriptionSpec struct {
	Tag       string `yaml:"tag"`             // Instance Tag in the device (e.g. master_level)
	Attribute string `yaml:"attribute"`       // attribute to watch (mute, level, callState, …)
	Channel   int    `yaml:"channel"`         // 1-based channel index
	Label     string `yaml:"label"`           // metric name surfaced in telemetry/events
	Rate      int    `yaml:"rate,omitempty"`  // optional min update rate in ms (defaults to 500)
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	expanded := os.ExpandEnv(string(data))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	applyDefaults(&cfg)
	return &cfg, validate(&cfg)
}

func applyDefaults(cfg *Config) {
	if cfg.Hub.ListenAddr == "" {
		cfg.Hub.ListenAddr = "0.0.0.0:8080"
	}
	if cfg.Hub.HeartbeatPeriod == 0 {
		cfg.Hub.HeartbeatPeriod = 30 * time.Second
	}
	if cfg.Hub.LogLevel == "" {
		cfg.Hub.LogLevel = "info"
	}
	if cfg.Hub.StorePath == "" {
		cfg.Hub.StorePath = "/var/lib/av-bridge/state.json"
	}
	if cfg.Hub.DeviceSyncInterval == 0 {
		cfg.Hub.DeviceSyncInterval = 5 * time.Minute
	}
	if cfg.Cloud.PushInterval == 0 {
		cfg.Cloud.PushInterval = 30 * time.Second
	}
	if cfg.Cloud.RetryAttempts == 0 {
		cfg.Cloud.RetryAttempts = 3
	}
	if cfg.Cloud.RetryDelay == 0 {
		cfg.Cloud.RetryDelay = 5 * time.Second
	}
	if cfg.Cloud.CommandPollInterval == 0 {
		cfg.Cloud.CommandPollInterval = 5 * time.Second
	}
	if cfg.Cloud.CommandMaxBatch == 0 {
		cfg.Cloud.CommandMaxBatch = 10
	}
	if cfg.Lens.PollInterval == 0 {
		cfg.Lens.PollInterval = 5 * time.Minute
	}
	if cfg.Alerts.OfflineAfter == 0 {
		cfg.Alerts.OfflineAfter = 5 * time.Minute
	}
	if cfg.Alerts.DegradedAfter == 0 {
		cfg.Alerts.DegradedAfter = 2 * time.Minute
	}
	if cfg.Alerts.RepeatInterval == 0 {
		cfg.Alerts.RepeatInterval = 30 * time.Minute
	}
	for i := range cfg.Devices {
		if cfg.Devices[i].PollRate == 0 {
			cfg.Devices[i].PollRate = 60 * time.Second
		}
		if cfg.Devices[i].BaudRate == 0 {
			cfg.Devices[i].BaudRate = 9600
		}
	}
}

func validate(cfg *Config) error {
	if cfg.Cloud.WebhookURL == "" {
		return fmt.Errorf("cloud.webhook_url is required")
	}
	if cfg.API.TLS.Enabled {
		if cfg.API.TLS.CertFile == "" || cfg.API.TLS.KeyFile == "" {
			return fmt.Errorf("api.tls: cert_file and key_file are required when TLS is enabled")
		}
	}
	seen := map[string]bool{}
	for i, d := range cfg.Devices {
		if d.ID == "" {
			return fmt.Errorf("devices[%d]: id is required", i)
		}
		if seen[d.ID] {
			return fmt.Errorf("duplicate device id %q", d.ID)
		}
		seen[d.ID] = true
		if !map[string]bool{"rest": true, "websocket": true, "telnet": true, "serial": true, "tesira": true, "sony_bravia": true, "poly_videoos": true, "aurora_rxt": true, "ping": true}[d.Protocol] {
			return fmt.Errorf("device %q: unsupported protocol %q", d.ID, d.Protocol)
		}
		if !map[string]bool{"display": true, "conferencing": true, "audio": true, "camera": true, "control": true}[d.Type] {
			return fmt.Errorf("device %q: unsupported type %q", d.ID, d.Type)
		}
	}
	return nil
}
