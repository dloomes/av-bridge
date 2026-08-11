# Adding an adapter

A step-by-step for wiring a new vendor integration (or a generic transport) into the bridge, cloud, and portal. If you follow the checklist end-to-end you'll spend roughly two hours: half of it reading the vendor's protocol doc, half writing Go.

## Concept map

| Piece | Lives in | What it does |
| --- | --- | --- |
| **Adapter** | `av-bridge/internal/device/adapters/<name>.go` | The Go type that owns the transport (telnet / REST / WebSocket / serial), issues polls, translates named commands, and declares `Capabilities()`. |
| **Factory** | `av-bridge/internal/device/adapters/factory.go` | Maps a protocol string from config to the adapter constructor. |
| **Config allowlist** | `av-bridge/internal/config/config.go` | Rejects a device with an unknown protocol at bridge startup. |
| **Cloud catalogue** | `av-bridge-cloud/internal/adapters/catalogue.go` | Single source of truth for the portal — vendor, description, config schema, example YAML, commands, metrics. |
| **Contract test** | `av-bridge-cloud/internal/adapters/contract_test.go` | Fails CI if factory and catalogue drift. Never write around it — it exists to catch this exact class of bug. |

The portal has **no** adapter list of its own — it fetches `/api/v1/adapters` and drives every picker from that. Nothing to add on the portal side unless the adapter needs a bespoke UI (rare).

## Checklist

Do these in order. Skipping ahead is how you end up with an adapter that compiles but the portal can't select or the cloud rejects.

### 1. Read the vendor's protocol document end-to-end

Before touching Go, know these six things:

- **Transport.** Telnet? REST? WebSocket? Serial? What port, what auth?
- **Session lifecycle.** Any login handshake? Session tokens? Keep-alive requirement?
- **How to read state.** One call or many? Polling model or push notifications?
- **How to send commands.** Command names, arg shape, return shape.
- **Failure modes.** What happens when the device reboots mid-conversation? Rate limits? Ambiguous error responses?
- **Encoder/decoder or role-based split.** Some devices behave differently by mode (Aurora VPX). Decide up front if you need mode gating.

Save the vendor PDF alongside the adapter Go file if the doc is fiddly — future you will thank you.

### 2. Write the adapter Go file

Copy the closest existing adapter as a starting point:

| Vendor style | Best model |
| --- | --- |
| JSON over Telnet | [`aurora_vpx.go`](../av-bridge/internal/device/adapters/aurora_vpx.go) or [`aurora_rxt.go`](../av-bridge/internal/device/adapters/aurora_rxt.go) |
| REST with session/XSRF | [`poly_videoos.go`](../av-bridge/internal/device/adapters/poly_videoos.go) |
| REST with pre-shared key | [`sony_bravia.go`](../av-bridge/internal/device/adapters/sony_bravia.go) |
| Subscription-based push telemetry | [`tesira.go`](../av-bridge/internal/device/adapters/tesira.go) |
| No control API (reachability only) | [`ping.go`](../av-bridge/internal/device/adapters/ping.go) |

Every adapter must:

- Embed `device.Base` so it inherits `Emit`, `Status`, `Events`.
- Implement `Connect`, `Disconnect`, `Poll`, `SendCommand`, `Capabilities`, `Status`.
- Serialise access to the shared connection with a `cmdMu sync.Mutex` — Poll and SendCommand share the wire and must not interleave.
- Set a per-request deadline on writes and reads. A hung device must not stall the poll goroutine.
- Return the connection to a known-good state after a failure. If a command exits abnormally, close the socket and let the next Poll reconnect — don't leave a half-consumed decoder behind.

**Gotchas the existing adapters have already been bitten by:**

- **Reboot commands close the socket mid-reply.** Don't `Decode` unconditionally — treat EOF after a successful write to `reboot` as normal. See [aurora_vpx.go's `sendReboot`](../av-bridge/internal/device/adapters/aurora_vpx.go).
- **Encoder/decoder mode is runtime-mutable.** If the device can flip modes, re-detect on every Poll rather than caching at Connect time.
- **Vendor product names ≠ manufacturer.** Sony's `product` tag says "BRAVIA" — that's a product line, not the vendor. Use the catalogue's vendor field for defaults.

### 3. Declare `Capabilities()`

The single most important thing an adapter does after actually talking to the device is declare what it can do. This drives:

- Which buttons show in the portal's Command panel
- Which step types the routine builder can offer for this device
- Metric column headers, alert rules, historical charts

Convention (see [device.go](../av-bridge/internal/device/device.go) for the type):

```go
var myAdapterCapabilities = device.Capabilities{
    Power:    device.PowerCapability{On: true, Off: true}, // does the adapter really support these?
    Commands: []string{"power_on", "power_off", "mute", "unmute"},
    Metrics:  []string{"power_status", "mute_state", "response_ms"},
}

func (a *MyAdapter) Capabilities() device.Capabilities {
    return myAdapterCapabilities
}
```

If commands are user-defined per device (Tesira), leave `Commands: nil` and build the list from `a.Cfg.Commands` at call time. Set `DynamicCommands: true` in the catalogue so the portal renders the right hint.

### 4. Register in the bridge factory

[`factory.go`](../av-bridge/internal/device/adapters/factory.go):

```go
case "my_adapter":
    return NewMyAdapter(cfg), nil
```

### 5. Add to the bridge config allowlist

[`config.go`](../av-bridge/internal/config/config.go) — the map inside `validate` that rejects unknown protocols. Add `"my_adapter": true` to keep parity with the factory. This is the last file in the bridge you touch.

### 6. Add the cloud catalogue entry

[`av-bridge-cloud/internal/adapters/catalogue.go`](../av-bridge-cloud/internal/adapters/catalogue.go) — this is the biggest chunk of the work, and the one where thoughtful copy pays off. Every field is user-visible.

```go
{
    ID:          "my_adapter",
    Name:        "Human Product Name (e.g. \"MyCorp XR-200 Displays\")",
    Vendor:      "MyCorp",
    Kind:        KindVendor,        // or KindTransport / KindProbe
    Description: "One-to-two-sentence what this adapter does and how it talks to the device.",
    DeviceTypes: []string{"display"},
    Power:       PowerCapability{On: true, Off: true},
    Commands:    []string{"power_on", "power_off", ...},   // match Capabilities().Commands exactly
    Metrics:     []string{"power_status", ...},            // match Capabilities().Metrics exactly
    ConfigSchema: []ConfigField{
        {Name: "address", Required: true, Description: "Base URL of the device.", Example: "http://192.168.1.10"},
        {Name: "password", Required: true, Description: "Device admin password."},
    },
    ExampleConfig: `- id: my-device
  name: Boardroom Display
  type: display
  protocol: my_adapter
  address: http://192.168.1.10
  password: ${MY_PASSWORD}`,
    DocsURL: "https://vendor.example.com/docs",   // optional
},
```

**Field-by-field guidance:**

- **`Name`** appears in the portal protocol picker and on the /adapters page card. Write it as a product name a facilities manager would recognise, not the wire protocol.
- **`Kind`** decides which section of the picker the entry lands in. Use `KindVendor` for anything with a vendor name attached, `KindTransport` for generic building blocks (rest/telnet/serial/ws), `KindProbe` for reachability checks.
- **`DeviceTypes`** must match one of the five allowed categories (display / conferencing / audio / camera / control). This is what shows in the "device type" chip.
- **`Commands` / `Metrics`** must mirror what `Capabilities()` returns on the bridge. If they drift, the /adapters page will lie and the routine builder will offer step types the runtime will reject.
- **`ExampleConfig`** ships as-is into the /adapters page's copy-to-clipboard box. Make it valid YAML the operator can paste into their `config.yaml`.

### 7. Run the tests

```
cd av-bridge && go test ./... && cd ..
cd av-bridge-cloud && go test ./... && cd ..
cd av-bridge-portal && npx tsc --noEmit && cd ..
```

The **contract test** ([`av-bridge-cloud/internal/adapters/contract_test.go`](../av-bridge-cloud/internal/adapters/contract_test.go)) will fail if step 4 or step 6 is missing. Read its error message — it tells you exactly which side you forgot.

### 8. Smoke-test against a real device

There's no substitute. Start the bridge locally with a device entry pointed at the physical unit, watch the journal for:

- Successful Connect and first Poll
- Metrics flowing through to the portal
- Every command in the palette actually executes end-to-end
- Reconnect after the device reboots

If the device isn't at hand, at least dry-run with `-config` pointing at a YAML that includes the new device and confirm the bridge factory picks it up (`level=INFO msg="adapter connected"` in the logs).

### 9. Commit and tag

Follow the existing style: one commit per logical concern.

```
git add av-bridge/internal/device/adapters/my_adapter.go \
        av-bridge/internal/device/adapters/factory.go \
        av-bridge/internal/config/config.go \
        av-bridge-cloud/internal/adapters/catalogue.go
git commit -m "bridge + cloud: my_adapter — [MyCorp XR-200] adapter"

git tag v0.x.y && git push --tags
```

The GitHub Actions release workflow builds signed Linux binaries. Once it's green, curl the new binary onto each collector:

```bash
sudo curl -fsSL -o /usr/local/bin/av-bridge.new \
  https://github.com/dloomes/av-bridge/releases/download/v0.x.y/av-bridge-linux-amd64
sudo chmod +x /usr/local/bin/av-bridge.new
sudo mv /usr/local/bin/av-bridge.new /usr/local/bin/av-bridge
sudo systemctl restart av-bridge
```

Rebuild the cloud container (`docker compose … up -d --build cloud`) so `/api/v1/adapters` returns the new entry. The portal picks up the new adapter automatically on next page load — no rebuild needed if you're running the dev server, no deploy needed once the API returns the entry.

## Updating an existing adapter

Adding a metric, renaming a command, tightening a capability declaration — same principles as adding a new one, with two extra warnings:

- **Renaming a metric.** Anything downstream (routines that `check_metric`, alerts, charts) references the old name. Do a global grep for the string before you rename, or leave a deprecation alias in the adapter and delete it in a later release.
- **Removing a command.** Any routine step of type `device_command` with that command name will fail at runtime. Search `nightly_routines.steps` in the DB (or the routine builder JSON view) before you delete.

The catalogue's `Commands` / `Metrics` fields are the single point of truth — if you change them there and in `Capabilities()` in the same commit, `go test ./...` catches the drift. If you change one and not the other, the contract test won't flag it (it only checks IDs) — that's a class of bug worth tightening in a future sprint if it bites.

## When the guidance above doesn't fit

Some vendors need bespoke handling that doesn't slot into the checklist. Rules of thumb:

- **Adapter needs a whole new metric type** (waveform data, video snapshots, RTP stats) — that's a bigger conversation about how telemetry is stored. Bring it up before writing the adapter.
- **Adapter needs a bespoke portal page** (interactive matrix routing, in-portal live audio meters) — it's still fine to write the adapter, but flag the UI need separately so it doesn't get bundled into "adapter work."
- **Adapter is one-of** (single site, one customer) — consider a `custom_rest` tag rather than a whole new protocol. Fewer moving parts.
