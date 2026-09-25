"""Generate the Mintlify "Supported devices" section from the adapter catalogue.

The catalogue (av-bridge-cloud/internal/adapters/catalogue.go) is the single
source of truth for what each adapter does — the portal's Adapters page reads
the same data. `go run ./cmd/adapter-catalogue` exports it as JSON; this module
turns that into one page per adapter plus an overview.

SUPPLEMENT holds the few things the catalogue doesn't: supported models,
what to enable on the device first, and customer-facing notes. Keep it short
and factual; everything else should be changed in the catalogue itself.
"""
from __future__ import annotations

import json
import re
import shutil
import subprocess
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent.parent
CACHE = HERE / "adapters.json"
SECTION_DIR = "devices"

# Command names the portal prompts a value for, and what it asks. Mirrors
# COMMAND_PROMPTS in av-bridge-portal/src/components/command-panel.tsx.
PROMPTS = {
    "preset_recall": "Asks for the preset number (0–255).",
    "preset_set": "Asks for the preset number to save (0–255).",
    "zoom_direct": "Asks for the zoom position (0 = wide, 16384 = full tele).",
    "outlet_on": "Asks for the outlet number.",
    "outlet_off": "Asks for the outlet number.",
    "outlet_reboot": "Asks for the outlet number (power-cycles it).",
    "dial": "Asks for the address to dial: SIP URI, H.323 address or phone number.",
}

PORTAL_FIELD = {
    "address": "Address",
    "username": "Username",
    "password": "Password",
    "poll_rate": "Poll rate (seconds)",
    "baud_rate": "Baud rate (serial)",
    "commands": "Commands",
    "subscriptions": "Readings",
}

DEVICE_TYPE = {
    "display": "Display",
    "conferencing": "Conferencing",
    "audio": "Audio",
    "camera": "Camera",
    "control": "Control",
}

SUPPLEMENT: dict[str, dict] = {
    "sony_bravia": {
        "sidebar": "Sony Bravia", "icon": "tv",
        "models": "Sony Bravia Professional Displays with IP control.",
        "connection": "JSON-RPC over HTTP, with Wake-on-LAN for power-on",
        "before": [
            "On the display, open **Settings → Network → Home network setup → IP control**.",
            "Set **Authentication** to **Normal and Pre-Shared Key** and enter a pre-shared key. You'll use it as the device's password.",
            "Turn on **Remote start** so the display can be powered on over the network from standby.",
        ],
        "notes": [
            "> **Power-on:** M.A.R.C.U.S. sends a Wake-on-LAN packet straight to the display's address, then the IP-control power command. The display's MAC address is learned automatically the first time it's reached while switched on; set the `mac_address` tag if you need power-on to work before that.",
        ],
    },
    "poly_videoos": {
        "sidebar": "Poly VideoOS", "icon": "video",
        "models": "Poly Studio X30, X50, X52 and X70, and the G7500.",
        "connection": "REST API over HTTPS, using a local admin session",
        "before": [
            "Make sure the codec has a local admin account, and note its username and password.",
            "Check the Collector can reach the codec's web interface on HTTPS (port 443).",
        ],
        "notes": [
            "> **Microsoft Teams Rooms and Zoom Rooms:** when the codec runs in appliance mode, its API is read-only. Readings still update, but the portal disables the command buttons.",
        ],
    },
    "tesira": {
        "sidebar": "Biamp Tesira", "icon": "sliders",
        "models": "Biamp Tesira DSPs, including TesiraFORTÉ, over the Tesira Text Protocol (TTP).",
        "connection": "TTP over Telnet (TCP 23)",
        "before": [
            "Make sure Telnet is enabled on the Tesira, which is the default.",
            "If the Tesira has login security turned on, note a username and password to enter on the device.",
            "In Tesira software, find the **instance tags** of the blocks you want to control or read, such as a level block or a meter.",
        ],
        "readings_text": (
            "Every Tesira reports its identity and health on each poll: hostname, part number, "
            "serial number, software version, IP and MAC address, fault count and the active fault list.\n\n"
            "Readings from your own DSP blocks are added per device in the **Readings** section of the device form:\n\n"
            "- **Add meter readings** takes a meter block's instance tag and a channel range such as `1-8`, and adds one reading per channel.\n"
            "- **Snapshot** reads a value on every poll, which suits meters. **Live** updates the moment a value changes and records each change as an event, which suits mute or call state.\n"
        ),
        "commands_text": (
            "Tesira commands are TTP strings you define per device in the **Commands** section of the device form. Each command becomes a button on the device page.\n\n"
            "- **Add standard commands** takes a level or mute block's instance tag, channel and step size, and adds mute, unmute, toggle mute, volume up/down and set level.\n"
            "- **Add preset recall** adds `DEVICE recallPreset {preset}`.\n"
            "- Put `{name}` in a command to ask for a value when the button is pressed, for example `master_level set level 1 {level}`.\n\n"
            "The TTP form is `<instance tag> <verb> <attribute> <channel> [value]`, for example `master_level set mute 1 true`."
        ),
    },
    "visca_over_ip": {
        "sidebar": "PTZ cameras (VISCA)", "icon": "camera",
        "models": "PTZ cameras that speak Sony VISCA-over-IP, including Sony BRC and SRG, Panasonic AW-UE, PTZOptics, HuddleCam, Marshall CV and Lumens VC-A61P.",
        "connection": "VISCA-over-IP (UDP 52381)",
        "before": [
            "Enable **VISCA over IP** in the camera's network settings, if it isn't on by default.",
            "Check the Collector can reach the camera on UDP 52381.",
        ],
    },
    "aurora_rxt": {
        "sidebar": "Aurora RXT panels", "icon": "tablet-screen-button",
        "models": "Aurora Multimedia RXT-x wall-mount touch panels.",
        "connection": "JSON-RPC over Telnet (TCP 6975)",
        "before": [
            "Check the Collector can reach the panel on TCP 6975.",
            "If the panel is secured, note its login username and password.",
        ],
        "notes": [
            "> **Touch-panel web UI:** if the portal user's browser can't reach the panel directly, the device page can open its web interface through the Collector. See the Collector's local URL setting on the Collectors page.",
        ],
    },
    "aurora_vpx": {
        "sidebar": "Aurora VPX", "icon": "tower-broadcast",
        "models": "Aurora Multimedia VPX-series AV-over-IP encoders and decoders.",
        "connection": "JSON over Telnet (TCP 6970)",
        "before": [
            "Check the Collector can reach the unit on TCP 6970. Encoder or decoder mode is detected automatically.",
        ],
    },
    "aten_pdu": {
        "sidebar": "ATEN eco PDU", "icon": "plug",
        "models": "ATEN eco PDU range, such as the PE6108G.",
        "connection": "Telnet CLI (TCP 23)",
        "before": [
            "Enable Telnet on the PDU and note an admin username and password.",
            "For models with more than 8 outlets, set the `outlet_count` tag on the device.",
        ],
        "notes": [
            "> **Power readings:** per-outlet current and power show the live draw of whatever is plugged in. Energy estimates in Reports use the **Power on** and **Power standby** wattage set on each device's own record.",
        ],
    },
    "rest": {
        "sidebar": "Generic REST", "icon": "code",
        "models": "Any device with a plain HTTP or JSON status endpoint.",
        "connection": "HTTP or HTTPS polling",
        "before": ["Find the URL of an endpoint that returns the device's status."],
    },
    "websocket": {
        "sidebar": "Generic WebSocket", "icon": "bolt",
        "models": "Any device that pushes its state as JSON over a WebSocket.",
        "connection": "WebSocket (`ws://` or `wss://`)",
        "before": ["Find the device's WebSocket URL."],
    },
    "telnet": {
        "sidebar": "Generic Telnet", "icon": "terminal",
        "models": "Control processors, matrix switchers, older displays and other devices with a line-based Telnet interface.",
        "connection": "Telnet (TCP, the port you specify)",
        "before": ["Find the device's Telnet port and the command strings it accepts."],
        "commands_text": (
            "Commands are strings you define per device in the **Commands** section of the device form. Each one becomes a button on the device page. "
            "Put `{name}` in a command to ask for a value when the button is pressed."
        ),
    },
    "serial": {
        "sidebar": "Generic RS-232", "icon": "microchip",
        "models": "Devices wired to the Collector host by RS-232.",
        "connection": "RS-232 serial on the Collector host",
        "before": [
            "Connect the device to a serial port on the Collector host. That means the Collector runs on a machine next to the device.",
            "Note the port name: `/dev/ttyUSB0` or similar on Linux, `COM3` or similar on Windows.",
        ],
    },
    "ping": {
        "sidebar": "ICMP ping", "icon": "wave-square",
        "models": "Anything with an IP address, such as a network switch, an unmanaged panel or an IP camera without a supported API.",
        "connection": "ICMP echo",
        "before": ["Make sure ICMP isn't blocked between the Collector and the device."],
        "notes": [
            "> **Windows Collectors:** set the tag `ping_privileged` to `true` on each ping device. Windows only allows raw ICMP, and the Collector service already runs with the rights it needs.",
        ],
    },
}


def load_catalogue() -> list[dict]:
    """Export the live catalogue with Go when available; else use the cache."""
    cloud = REPO / "av-bridge-cloud"
    if shutil.which("go") and cloud.exists():
        res = subprocess.run(["go", "run", "./cmd/adapter-catalogue"], cwd=cloud,
                             capture_output=True, text=True, encoding="utf-8")
        if res.returncode == 0:
            CACHE.write_text(res.stdout, encoding="utf-8")
            return json.loads(res.stdout)
        print(f"warning: adapter-catalogue failed, using cached {CACHE.name}: {res.stderr.strip()}")
    return json.loads(CACHE.read_text(encoding="utf-8"))


def pretty(name: str) -> str:
    return re.sub(r"\b\w", lambda m: m.group(0).upper(), name.replace("_", " "))


def collapse(names: list[str]) -> list[str]:
    """outlet_1_state … outlet_8_state -> outlet_N_state (order kept)."""
    out: list[str] = []
    for n in names:
        c = re.sub(r"_\d+(?=_|$)", "_N", n)
        if c not in out:
            out.append(c)
    return out


def portal_wording(text: str) -> str:
    """Catalogue descriptions use Collector-YAML notation ("tags.x: 'y'");
    customers use the portal form, so name tags the way the form does."""
    text = re.sub(r"tags\.(\w+):\s*'([^']*)'", r"the `\1` tag set to `\2`", text)
    return re.sub(r"tags\.(\w+)", r"the `\1` tag", text)


def md_cell(text: str) -> str:
    return text.replace("|", "\\|").replace("\n", " ")


def page_markdown(a: dict, sup: dict) -> str:
    kind_label = {"vendor": "Vendor adapter", "transport": "Generic adapter", "probe": "Probe"}[a["kind"]]
    lines: list[str] = []
    if sup.get("models"):
        lines += [f"**Works with:** {sup['models']}", ""]

    types = ", ".join(DEVICE_TYPE.get(t, t) for t in a["device_types"])
    power = "Yes" if a["power"]["on"] and a["power"]["off"] else "No"
    lines += [
        "| | |", "|---|---|",
        f"| **Type** | {kind_label} |",
    ]
    if a.get("vendor"):
        lines.append(f"| **Vendor** | {md_cell(a['vendor'])} |")
    lines += [
        f"| **Device types** | {types} |",
        f"| **Connection** | {md_cell(sup.get('connection', ''))} |",
        f"| **Power on/off from the portal** | {power} |",
        f"| **Protocol in the portal** | {md_cell(a['name'])} (`{a['id']}`) |",
        "",
    ]

    if sup.get("before"):
        lines += ["## Before you start", ""]
        lines += [f"{i}. {s}" for i, s in enumerate(sup["before"], 1)]
        lines.append("")

    lines += [
        "## Add the device", "",
        "1. In the portal, go to **Devices** and select **New device**.",
        f"2. Choose the **Collector** that can reach the device, and set **Protocol** to **{md_cell(a['name'])}**.",
        "3. Enter the settings below and save. The Collector picks up the new device within five minutes.",
        "",
        "| Setting | Required | What to enter | Example |", "|---|---|---|---|",
    ]
    tag_example = None
    for f in a["config_schema"]:
        name = f["name"]
        if name.startswith("tags."):
            if tag_example is None and "_N_" not in name:
                tag_example = (name[5:], (f.get("example") or "value"))
            field = f"Tags → `{name[5:]}`"
        else:
            field = PORTAL_FIELD.get(name, pretty(name))
        example = f.get("example", "") or ""
        if example.startswith("${"):
            example = ""
        if name == "poll_rate" and example.endswith("s"):
            example = example[:-1]
        ex = f"`{example}`" if example else ""
        lines.append(f"| **{field}** | {'Yes' if f['required'] else 'No'} | {md_cell(portal_wording(f['description']))} | {ex} |")
    lines.append("")
    if tag_example:
        key, val = tag_example
        lines += [
            "> **Tags** are set under **Advanced (JSON) → Tags** in the device form, as a JSON object, "
            f"for example `{{\"{key}\": \"{val}\"}}`.",
            "",
        ]

    lines += ["## Readings", ""]
    if sup.get("readings_text"):
        lines += [sup["readings_text"], ""]
    elif a.get("metrics"):
        lines += ["These values appear under **Device Information** on the device page, and can be checked in Room Readiness routines.", "",
                  "| Reading | Shown as |", "|---|---|"]
        for m in collapse(a["metrics"]):
            lines.append(f"| `{m}` | {pretty(m)} |")
        lines.append("")
    else:
        lines += ["Reachability and response time are recorded on every poll. The device's own values depend on what its endpoint returns.", ""]

    lines += ["## Commands", ""]
    if sup.get("commands_text"):
        lines += [sup["commands_text"], ""]
    elif a.get("commands"):
        lines += ["Each command is a button on the device page, and can be used in Room Readiness routines.", "",
                  "| Command | Button | Notes |", "|---|---|---|"]
        for c in a["commands"]:
            lines.append(f"| `{c}` | {pretty(c)} | {PROMPTS.get(c, '')} |")
        lines.append("")
    else:
        lines += ["This adapter monitors only; it has no commands.", ""]

    for n in sup.get("notes", []):
        lines += [n, ""]
    return "\n".join(lines)


def build(out_dir: Path, convert, frontmatter, yaml_str) -> tuple[list[Path], dict]:
    """Write the section; returns (files written, docs.json nav group)."""
    cat = load_catalogue()
    missing = [a["id"] for a in cat if a["id"] not in SUPPLEMENT]
    if missing:
        raise SystemExit(f"adapter_docs: add SUPPLEMENT entries for {missing} "
                         "(new adapter in the catalogue)")
    base = out_dir / SECTION_DIR
    base.mkdir(parents=True, exist_ok=True)
    written = []
    vendor, generic = [], []
    for a in cat:
        sup = SUPPLEMENT[a["id"]]
        slug = a["id"].replace("_", "-")
        meta = {"title": a["name"], "sidebarTitle": sup["sidebar"],
                "description": a["description"], "icon": sup["icon"]}
        dest = base / f"{slug}.mdx"
        dest.write_text(frontmatter(meta) + convert(page_markdown(a, sup)), encoding="utf-8", newline="\n")
        written.append(dest)
        (vendor if a["kind"] == "vendor" else generic).append((a, sup, f"{SECTION_DIR}/{slug}"))

    # Overview page.
    ov = [
        "M.A.R.C.U.S. talks to each device using its own control protocol, through the Collector on your network. "
        "Nothing on the device needs to reach the internet. Each **adapter** below supports a family of devices. "
        "You choose it as the device's **Protocol** when you add the device in the portal.",
        "",
        "## Generic adapters",
        "",
        "When there's no vendor adapter for a device, a generic adapter can usually still monitor it, and often control it too.",
        "",
        "| Adapter | Use it for |", "|---|---|",
    ]
    for a, sup, path in generic:
        ov.append(f"| [{md_cell(a['name'])}](/{path}) | {md_cell(sup['models'])} |")
    ov += [
        "",
        "## Don't see your device?",
        "",
        "New vendor adapters are added in most releases, and customer requests set the priorities. "
        "Contact commercial@involve.vc with the make and model you need.",
    ]
    cards = ["<CardGroup cols={2}>"]
    for a, sup, path in vendor:
        cards += [f'  <Card title={yaml_str(a["name"])} icon={yaml_str(sup["icon"])} href={yaml_str("/" + path)}>',
                  f"    {sup['models']}", "  </Card>"]
    cards.append("</CardGroup>")
    body = convert("\n".join(ov))
    # Vendor cards go first, under their own heading.
    body = "## Vendor adapters\n\n" + "\n".join(cards) + "\n\n" + body
    ov_meta = {"title": "Supported devices", "sidebarTitle": "Overview",
               "description": "The device adapters M.A.R.C.U.S. supports, and how to add each kind of device.",
               "icon": "plug-circle-check"}
    dest = base / "overview.mdx"
    dest.write_text(frontmatter(ov_meta) + body, encoding="utf-8", newline="\n")
    written.insert(0, dest)

    nav = {
        "group": "Supported devices",
        "icon": "plug-circle-check",
        "pages": [
            f"{SECTION_DIR}/overview",
            {"group": "Vendor adapters", "pages": [p for _, _, p in vendor]},
            {"group": "Generic adapters", "pages": [p for _, _, p in generic]},
        ],
    }
    return written, nav
