// Package adapters is the cloud-side single-source-of-truth for the set of
// device adapters the platform supports. The bridge factory (in a separate
// Go module) still switches on protocol strings to wire each adapter to a
// constructor — that's genuine code, not config — but every other place
// that needs to know "which protocols exist / what does each do" reads
// from this catalogue.
//
// When you add a new adapter to the bridge (av-bridge/internal/device/
// adapters/factory.go), add its entry here in the same commit. The cloud's
// protocol allowlist reads from Catalogue(), so an adapter missing from
// this file cannot be selected in the portal even if the bridge supports it.
package adapters

// Kind distinguishes vendor-specific integrations from generic transport
// primitives and standalone probes. Purely a UI/documentation signal.
type Kind string

const (
	KindVendor    Kind = "vendor"    // Biamp Tesira, Sony Bravia, Poly VideoOS, Aurora…
	KindTransport Kind = "transport" // rest, websocket, telnet, serial — generic building blocks
	KindProbe     Kind = "probe"     // ping — network reachability, no vendor semantics
)

// PowerCapability mirrors device.PowerCapability from the bridge. Duplicated
// here to keep the cloud module free of a dependency on the bridge module.
type PowerCapability struct {
	On  bool `json:"on"`
	Off bool `json:"off"`
}

// ConfigField describes one YAML config key an operator needs to set when
// adding a device that uses this adapter. Used by the /adapters page to
// render a schema table + copyable example config.
type ConfigField struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Example     string `json:"example,omitempty"`
}

// Info is the public shape returned by GET /api/v1/adapters. DeviceCount
// is populated per-request by joining against the devices table; the
// static definition below leaves it zero.
type Info struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Vendor          string          `json:"vendor,omitempty"`
	Kind            Kind            `json:"kind"`
	Description     string          `json:"description"`
	DeviceTypes     []string        `json:"device_types"`
	Power           PowerCapability `json:"power"`
	Commands        []string        `json:"commands,omitempty"`
	Metrics         []string        `json:"metrics,omitempty"`
	DynamicCommands bool            `json:"dynamic_commands,omitempty"`
	ConfigSchema    []ConfigField   `json:"config_schema"`
	ExampleConfig   string          `json:"example_config"`
	DocsURL         string          `json:"docs_url,omitempty"`
	DeviceCount     int             `json:"device_count"`
}

// Catalogue returns a copy of the static adapter registry. Callers may
// mutate the returned slice (e.g. to attach device counts) without
// affecting other requests.
func Catalogue() []Info {
	out := make([]Info, len(catalogue))
	copy(out, catalogue)
	return out
}

// IsSupportedProtocol reports whether id is a known adapter protocol.
// Cloud handlers use this in place of the historic hard-coded allowlist
// so adding an adapter to the catalogue automatically permits it in
// device-create/update payloads.
func IsSupportedProtocol(id string) bool {
	for _, a := range catalogue {
		if a.ID == id {
			return true
		}
	}
	return false
}

// SupportedProtocols returns just the ID strings — convenient for tests
// and error messages that want to list the valid options.
func SupportedProtocols() []string {
	out := make([]string, len(catalogue))
	for i, a := range catalogue {
		out[i] = a.ID
	}
	return out
}

// The catalogue itself. Order here is the display order in the portal —
// vendor integrations first (most interesting to users), then transports,
// then the ping probe. Keep alphabetical inside each group.
var catalogue = []Info{
	// ── Vendor-specific ──────────────────────────────────────────────

	{
		ID:          "aurora_rxt",
		Name:        "Aurora RXT-x Touch Panels",
		Vendor:      "Aurora Multimedia",
		Kind:        KindVendor,
		Description: "Wall-mount control touch panels. JSON-RPC over Telnet on port 6975, with proximity, lux, and relay control.",
		DeviceTypes: []string{"control"},
		Power:       PowerCapability{On: false, Off: false},
		Commands: []string{
			"reboot", "factory_default", "test_audio",
			"mute", "unmute",
			"auto_brightness_on", "auto_brightness_off",
			"relay_1_on", "relay_1_off", "relay_1_toggle",
			"relay_2_on", "relay_2_off", "relay_2_toggle",
		},
		Metrics: []string{
			"volume", "mute",
			"lcd_brightness", "lcd_timeout",
			"lux", "proximity",
			"auto_brightness",
			"relay_1", "relay_2",
			"event_manager",
			"response_ms",
		},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "Panel IP or hostname (port 6975 is implied).", Example: "192.168.1.50"},
			{Name: "username", Required: false, Description: "Login username if the panel is secured.", Example: "admin"},
			{Name: "password", Required: false, Description: "Login password if the panel is secured."},
		},
		ExampleConfig: `- id: reception-panel
  name: Reception Touch Panel
  type: control
  protocol: aurora_rxt
  address: 192.168.1.50
  poll_rate: 30s`,
	},

	{
		ID:          "aurora_vpx",
		Name:        "Aurora VPX Series",
		Vendor:      "Aurora Multimedia",
		Kind:        KindVendor,
		Description: "AV-over-IP encoders and decoders. JSON over Telnet on port 6970; encoder vs decoder mode is auto-detected at connect.",
		DeviceTypes: []string{"display"},
		Power:       PowerCapability{On: false, Off: false},
		Commands: []string{
			"reboot", "identify", "stop_identify",
			"mute", "unmute",
			"join", "leave",
			"start_stream", "stop_stream",
		},
		Metrics: []string{
			"state", "mode", "fw_version",
			"mac", "hostname", "ip_address", "ip_mode",
			"display_mode",
			"hdmi_hdcp", "hdmi_hpd_in1", "hdmi_hpd_in2",
			"local_display_source", "stream_source", "stream_status",
			"video_h_size", "video_v_size", "video_fps",
		},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "Encoder or decoder IP (port 6970 is implied).", Example: "192.168.10.20"},
			{Name: "poll_rate", Required: false, Description: "How often to poll. Defaults to 60s.", Example: "30s"},
		},
		ExampleConfig: `- id: boardroom-vpx
  name: Boardroom VPX Decoder
  type: display
  protocol: aurora_vpx
  address: 192.168.10.20
  poll_rate: 30s`,
	},

	{
		ID:          "poly_videoos",
		Name:        "Poly VideoOS",
		Vendor:      "HP / Poly",
		Kind:        KindVendor,
		Description: "Studio X and G7500 conferencing codecs. REST with session/XSRF login lifecycle; reads work in appliance (Teams / Zoom Rooms) mode, writes lock.",
		DeviceTypes: []string{"conferencing"},
		Power:       PowerCapability{On: false, Off: false},
		Commands: []string{
			"dial", "hangup",
			"mute", "unmute",
			"vol_up", "vol_dn",
			"reboot",
		},
		Metrics: []string{
			"call_state", "active_calls",
			"mic_mute", "volume",
			"device_mode_active",
			"response_ms",
		},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "Codec base URL including scheme.", Example: "https://192.168.20.30"},
			{Name: "username", Required: true, Description: "Local admin username.", Example: "admin"},
			{Name: "password", Required: true, Description: "Local admin password."},
		},
		ExampleConfig: `- id: studio-x70
  name: Boardroom Studio X70
  type: conferencing
  protocol: poly_videoos
  address: https://192.168.20.30
  username: admin
  password: ${POLY_PASSWORD}
  tags:
    lens_managed: "true"`,
		DocsURL: "https://docs.poly.com/",
	},

	{
		ID:          "sony_bravia",
		Name:        "Sony Bravia Professional",
		Vendor:      "Sony",
		Kind:        KindVendor,
		Description: "Bravia Professional Displays. JSON-RPC REST with Pre-Shared Key authentication and Wake-on-LAN power-on support.",
		DeviceTypes: []string{"display"},
		Power:       PowerCapability{On: true, Off: true},
		Commands: []string{
			"power_on", "power_off",
			"input_hdmi1", "input_hdmi2", "input_hdmi3", "input_hdmi4",
			"mute", "unmute",
			"volume_up", "volume_down",
		},
		Metrics: []string{
			"power_status", "power_saving_mode",
			"current_input", "input_title", "input_source",
			"volume_speaker", "mute_speaker",
			"response_ms",
		},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "Display base URL (http:// is fine on the LAN).", Example: "http://192.168.30.40"},
			{Name: "password", Required: true, Description: "Pre-Shared Key configured on the display."},
			{Name: "tags.mac_address", Required: false, Description: "MAC for Wake-on-LAN. Discovered automatically once the display is powered on; set explicitly if power_on must work from a cold start.", Example: "AA:BB:CC:11:22:33"},
		},
		ExampleConfig: `- id: waiting-room-tv
  name: Waiting Room Display
  type: display
  protocol: sony_bravia
  address: http://192.168.30.40
  password: ${BRAVIA_PSK}`,
	},

	{
		ID:              "tesira",
		Name:            "Biamp Tesira",
		Vendor:          "Biamp",
		Kind:            KindVendor,
		Description:     "Tesira DSPs over TTP (Telnet). Subscription-based metrics; commands are TTP strings you define per device.",
		DeviceTypes:     []string{"audio"},
		Power:           PowerCapability{On: false, Off: false},
		DynamicCommands: true,
		Commands:        nil, // user-defined per device
		Metrics:         nil, // user-defined per device via subscriptions
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "DSP IP (Telnet port 23 is implied).", Example: "192.168.40.10"},
			{Name: "commands", Required: false, Description: "Named TTP command map. Each entry becomes a button in the portal."},
			{Name: "subscriptions", Required: false, Description: "Push-notification subscriptions. Each becomes a live metric."},
		},
		ExampleConfig: `- id: main-hall-dsp
  name: Main Hall Tesira
  type: audio
  protocol: tesira
  address: 192.168.40.10
  commands:
    mute_all: master_level mute set 1
    unmute_all: master_level mute set 0
  subscriptions:
    - tag: master_level
      attribute: level
      channel: 1
      label: master_level_db
      rate: 500`,
	},

	{
		ID:          "visca_over_ip",
		Name:        "VISCA-over-IP (PTZ cameras)",
		Vendor:      "Sony standard",
		Kind:        KindVendor,
		Description: "Sony VISCA-over-IP on UDP 52381. Cross-vendor: works with Sony BRC/SRG, Panasonic AW-UE, PTZOptics, HuddleCam, Marshall CV, Lumens VC-A61P and other VISCA-compliant PTZ cameras.",
		DeviceTypes: []string{"camera"},
		Power:       PowerCapability{On: true, Off: true},
		Commands: []string{
			"power_on", "power_off",
			"zoom_stop", "zoom_tele", "zoom_wide", "zoom_direct",
			"focus_auto", "focus_manual", "focus_one_push",
			"preset_recall", "preset_set",
			"pan_left", "pan_right", "tilt_up", "tilt_down",
			"pan_tilt_stop", "pan_tilt_home",
		},
		Metrics: []string{"power", "zoom_position", "zoom_percent", "version", "response_ms"},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "Camera IP or host. Port 52381 is the Sony default; append :port to override.", Example: "192.168.50.20"},
			{Name: "poll_rate", Required: false, Description: "How often to inquire power/zoom. Defaults to 30s.", Example: "30s"},
		},
		ExampleConfig: `- id: boardroom-cam
  name: Boardroom PTZ
  type: camera
  protocol: visca_over_ip
  address: 192.168.50.20
  poll_rate: 30s`,
	},

	// ── Generic transports ──────────────────────────────────────────

	{
		ID:          "rest",
		Name:        "Generic REST",
		Kind:        KindTransport,
		Description: "HTTP GET-based polling for devices that expose a plain REST endpoint. Use as a starting point when no vendor adapter fits.",
		DeviceTypes: []string{"display", "conferencing", "audio", "camera", "control"},
		Power:       PowerCapability{On: false, Off: false},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "Full URL to the endpoint being polled.", Example: "http://192.168.50.10/api/status"},
			{Name: "username", Required: false, Description: "HTTP Basic username if the endpoint requires auth."},
			{Name: "password", Required: false, Description: "HTTP Basic password if the endpoint requires auth."},
		},
		ExampleConfig: `- id: custom-rest-device
  name: Custom REST Device
  type: control
  protocol: rest
  address: http://192.168.50.10/api/status
  poll_rate: 60s`,
	},

	{
		ID:          "websocket",
		Name:        "Generic WebSocket",
		Kind:        KindTransport,
		Description: "Long-lived WebSocket connection with periodic keep-alive probes. Suitable for devices that push state as JSON frames.",
		DeviceTypes: []string{"display", "conferencing", "audio", "camera", "control"},
		Power:       PowerCapability{On: false, Off: false},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "ws:// or wss:// URL of the endpoint.", Example: "wss://192.168.60.10/socket"},
		},
		ExampleConfig: `- id: custom-ws-device
  name: Custom WebSocket Device
  type: control
  protocol: websocket
  address: wss://192.168.60.10/socket`,
	},

	{
		ID:          "telnet",
		Name:        "Generic Telnet",
		Kind:        KindTransport,
		Description: "Raw line-oriented Telnet transport. Custom command strings sent as configured; useful for legacy AV gear.",
		DeviceTypes: []string{"display", "audio", "camera", "control"},
		Power:       PowerCapability{On: false, Off: false},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "host:port of the Telnet endpoint.", Example: "192.168.70.10:23"},
			{Name: "commands", Required: false, Description: "Named command strings sent on demand from the portal."},
		},
		ExampleConfig: `- id: custom-telnet-device
  name: Custom Telnet Device
  type: control
  protocol: telnet
  address: 192.168.70.10:23`,
	},

	{
		ID:          "serial",
		Name:        "Generic Serial (RS-232)",
		Kind:        KindTransport,
		Description: "Direct RS-232 to a device wired to the collector host. Baud rate configurable; commands sent as raw byte strings.",
		DeviceTypes: []string{"display", "audio", "camera", "control"},
		Power:       PowerCapability{On: false, Off: false},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "Serial device path on the host running the collector.", Example: "/dev/ttyUSB0"},
			{Name: "baud_rate", Required: false, Description: "Baud rate. Defaults to 9600.", Example: "115200"},
		},
		ExampleConfig: `- id: projector-rs232
  name: Auditorium Projector
  type: display
  protocol: serial
  address: /dev/ttyUSB0
  baud_rate: 9600`,
	},

	// ── Standalone probes ───────────────────────────────────────────

	{
		ID:          "ping",
		Name:        "ICMP Ping",
		Kind:        KindProbe,
		Description: "Network reachability probe. Sends ICMP echoes and records loss and latency. Use for devices with no vendor API — a switch, an unmanaged panel, an IP camera.",
		DeviceTypes: []string{"display", "conferencing", "audio", "camera", "control"},
		Power:       PowerCapability{On: false, Off: false},
		Commands:    []string{"ping"},
		Metrics: []string{
			"reachable",
			"packets_sent", "packets_recv", "packet_loss_pct",
			"response_ms", "min_ms", "max_ms",
		},
		ConfigSchema: []ConfigField{
			{Name: "address", Required: true, Description: "IP address or hostname to probe.", Example: "192.168.80.10"},
			{Name: "tags.ping_count", Required: false, Description: "Packets per probe cycle. Defaults to 3.", Example: "5"},
			{Name: "tags.ping_timeout_ms", Required: false, Description: "Per-packet timeout. Defaults to 1000ms.", Example: "2000"},
		},
		ExampleConfig: `- id: floor-2-switch
  name: Floor 2 Access Switch
  type: control
  protocol: ping
  address: 192.168.80.10
  poll_rate: 60s`,
	},
}
