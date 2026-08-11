package adapters

import (
	"fmt"

	"github.com/dloomes/av-bridge/internal/cloud/lens"
	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/device"
)

// Deps holds optional collaborators that some adapters use to enrich their
// data. Pass nil values for any that aren't configured.
type Deps struct {
	Lens *lens.Client
}

// New creates the appropriate Device adapter for the given config.
// Base transport adapters (rest, websocket, telnet, serial) handle generic
// devices. Vendor-specific adapters extend these for devices with non-standard
// authentication or control models.
//
// When adding a new adapter here, also add its metadata to the cloud
// catalogue at av-bridge-cloud/internal/adapters/catalogue.go — that file
// is the single source of truth for what the portal shows on /adapters,
// what protocol strings the /devices API accepts, and how operators are
// told to configure it. An adapter that lives here but not in the cloud
// catalogue cannot be selected in the portal.
func New(cfg config.DeviceConfig, deps Deps) (device.Device, error) {
	switch cfg.Protocol {
	// ── Base transport adapters ──────────────────────────────────────────────
	case "rest":
		return NewRESTAdapter(cfg), nil
	case "websocket":
		return NewWebSocketAdapter(cfg), nil
	case "telnet":
		return NewTelnetAdapter(cfg), nil
	case "serial":
		return NewSerialAdapter(cfg), nil

	// ── Vendor-specific adapters ─────────────────────────────────────────────
	case "tesira":
		// Biamp Tesira DSPs — TTP over Telnet with subscription support
		return NewTesiraAdapter(cfg), nil
	case "sony_bravia":
		// Sony Bravia Professional Displays — JSON-RPC REST with PSK auth
		return NewSonyBraviaAdapter(cfg), nil
	case "poly_videoos":
		// Poly VideoOS — G7500, Studio X70/X52/X50/X30; REST with session/XSRF lifecycle
		return NewPolyVideoOSAdapter(cfg, deps.Lens), nil
	case "aurora_rxt":
		// Aurora Multimedia RXT-x WM touch panels — JSON-RPC over Telnet (port 6975)
		return NewAuroraAdapter(cfg), nil
	case "aurora_vpx":
		// Aurora Multimedia VPX Series AV-over-IP encoders/decoders —
		// JSON over Telnet (port 6970); mode auto-detected at connect
		return NewAuroraVPXAdapter(cfg), nil
	case "ping":
		// ICMP reachability probe for devices with no vendor API
		return NewPingAdapter(cfg), nil

	default:
		return nil, fmt.Errorf("unsupported protocol: %q (supported: rest, websocket, telnet, serial, tesira, sony_bravia, poly_videoos, aurora_rxt, aurora_vpx, ping)", cfg.Protocol)
	}
}
