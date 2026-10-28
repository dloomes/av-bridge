// A Room Readiness routine as a flow diagram: an SVG document built as a
// string, so the same markup is shown on the routine page and downloaded
// as SVG or PNG. Light colours on white throughout, since the download
// has to read the same in a document whatever the portal's theme.
//
// What a failure does follows the executor (internal/nightly/executor.go):
// no on_failure or "abort" stops the run; "continue" carries on; "retry:N"
// tries again N times, then carries on. Any failed step fails the run.

import { DEVICE_TYPE_LABELS, type DeviceType } from "@/lib/types";
import { STEP_TYPE_LABEL, type Step, type Target } from "@/components/routine-builder/step-types";

export interface DiagramOptions {
  title: string;
  deviceName?: (id: string) => string | undefined;
}

const FONT = "'Segoe UI', Roboto, Helvetica, Arial, sans-serif";
const C = {
  ink: "#1b2230",
  muted: "#5f6b7d",
  rule: "#d8dde5",
  line: "#8a94a6",
  box: "#ffffff",
  section: "#f3f6f9",
  power: "#d98a1c",
  command: "#2f6fd6",
  check: "#1f9d55",
  wait: "#7a8597",
  fail: "#c0392b",
  failBg: "#fdecea",
  retry: "#b7791f",
  retryBg: "#fdf3e1",
  carry: "#5f6b7d",
  carryBg: "#eef1f5",
  ready: "#1f9d55",
  readyBg: "#e7f6ed",
};

// Layout, in px.
const W = 660;
const BOX_X = 70;
const BOX_W = 370;
const CX = BOX_X + BOX_W / 2;
const BRANCH_X = BOX_X + BOX_W + 34;
const BRANCH_W = W - BRANCH_X - 16;
const GAP = 30; // vertical space between nodes, for the connector

function esc(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// fit trims text to roughly what fits in width px at the given font size.
function fit(s: string, width: number, size: number): string {
  const max = Math.floor(width / (size * 0.56));
  return s.length > max ? s.slice(0, max - 1).trimEnd() + "…" : s;
}

function duration(seconds: number): string {
  if (seconds < 60) return `${seconds} s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s ? `${m} min ${s} s` : `${m} min`;
}

const OP: Record<string, string> = { gt: ">", gte: "≥", lt: "<", lte: "≤", eq: "=" };

function targetText(t: Target | undefined, o: DiagramOptions): string {
  if (!t) return "whole room";
  if ("device_id" in t && t.device_id) return o.deviceName?.(t.device_id) ?? "one device";
  if ("device_type" in t && t.device_type) {
    const label = DEVICE_TYPE_LABELS[t.device_type as DeviceType] ?? t.device_type;
    return `all ${label} devices`;
  }
  return "whole room";
}

// detail is a step's second line: what it does, and to which devices.
function detail(s: Step, o: DiagramOptions): string {
  switch (s.type) {
    case "expect_status":
      return `Expect ${s.status} · ${targetText(s.target, o)}`;
    case "check_metric":
      return `${s.metric || "?"} ${OP[s.operator] ?? s.operator} ${s.threshold} · ${targetText(s.target, o)}`;
    case "ping": {
      const limits = [
        s.max_loss_pct !== undefined ? `loss ≤ ${s.max_loss_pct}%` : "",
        s.max_response_ms !== undefined ? `≤ ${s.max_response_ms} ms` : "",
      ].filter(Boolean);
      return `${s.count ?? 3} pings${limits.length ? ` (${limits.join(", ")})` : ""} · ${targetText(s.target, o)}`;
    }
    case "power_on":
    case "power_off":
      return targetText(s.target, o);
    case "device_command":
      return `${s.command || "(no command)"} · ${targetText(s.target, o)}`;
    default:
      return "";
  }
}

function accent(s: Step): string {
  switch (s.type) {
    case "power_on":
    case "power_off":
      return C.power;
    case "device_command":
      return C.command;
    default:
      return C.check;
  }
}

type Failure = { kind: "stop" } | { kind: "carry" } | { kind: "retry"; times: number };

function failure(s: Step): Failure | null {
  if (s.type === "wait" || s.type === "section") return null;
  const p = (s.on_failure ?? "").toLowerCase().trim();
  if (p === "continue") return { kind: "carry" };
  const m = /^retry:(\d+)$/.exec(p);
  if (m) return { kind: "retry", times: Number(m[1]) };
  return { kind: "stop" };
}

function pill(x: number, y: number, w: number, h: number, fill: string, stroke: string, text: string, color: string, size = 12, bold = false): string {
  return (
    `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="${h / 2}" fill="${fill}" stroke="${stroke}" stroke-width="1.2"/>` +
    `<text x="${x + w / 2}" y="${y + h / 2 + size * 0.36}" text-anchor="middle" font-size="${size}"${bold ? ' font-weight="600"' : ""} fill="${color}">${esc(text)}</text>`
  );
}

function arrowDown(y1: number, y2: number): string {
  return `<line x1="${CX}" y1="${y1}" x2="${CX}" y2="${y2 - 1}" stroke="${C.line}" stroke-width="1.5" marker-end="url(#arrow)"/>`;
}

// routineDiagramSVG draws the routine top to bottom: start, each step
// (sections as bands around their steps), the failure branch beside each
// check or action, and the ready end point.
export function routineDiagramSVG(steps: Step[], o: DiagramOptions): string {
  const body: string[] = [];
  const bands: string[] = [];
  let y = 24;

  // Heading.
  body.push(`<text x="24" y="${y + 16}" font-size="18" font-weight="600" fill="${C.ink}">${esc(fit(o.title || "Routine", W - 48, 18))}</text>`);
  // Steps are numbered as on a run's results page, sections included.
  body.push(`<text x="24" y="${y + 36}" font-size="12" fill="${C.muted}">Room Readiness routine</text>`);
  y += 60;

  // Start.
  body.push(pill(CX - 50, y, 100, 30, C.box, C.line, "Start", C.ink, 12, true));
  y += 30;

  // An open section band: where it started and its label.
  let band: { top: number; label: string } | null = null;
  const closeBand = (bottom: number) => {
    if (!band) return;
    bands.push(
      `<rect x="${BOX_X - 46}" y="${band.top}" width="${W - (BOX_X - 46) - 8}" height="${bottom - band.top}" rx="10" fill="${C.section}" stroke="${C.rule}"/>` +
        `<text x="${BOX_X - 34}" y="${band.top + 18}" font-size="11" font-weight="600" letter-spacing="0.6" fill="${C.muted}">${esc(fit(band.label.toUpperCase(), W - BOX_X, 11))}</text>`
    );
    band = null;
  };

  if (steps.length === 0) {
    body.push(arrowDown(y, y + GAP));
    y += GAP;
    body.push(pill(CX - 80, y, 160, 30, C.carryBg, C.rule, "No steps yet", C.muted));
    y += 30;
  }

  steps.forEach((s, i) => {
    const num = String(i + 1);
    if (s.type === "section") {
      closeBand(y + 12);
      y += 18;
      band = { top: y, label: s.label || s.name || "Section" };
      y += 22;
      return;
    }

    body.push(arrowDown(y, y + GAP));
    y += GAP;

    if (s.type === "wait") {
      const text = `Wait ${duration(s.duration_seconds ?? 0)}`;
      body.push(`<text x="${BOX_X - 14}" y="${y + 19}" text-anchor="end" font-size="11" fill="${C.muted}">${num}</text>`);
      body.push(pill(CX - 75, y, 150, 28, C.box, C.wait, text, C.wait, 12));
      y += 28;
      return;
    }

    const h = 58;
    const title = s.name || STEP_TYPE_LABEL[s.type];
    const second = detail(s, o);
    body.push(`<text x="${BOX_X - 14}" y="${y + h / 2 + 4}" text-anchor="end" font-size="11" fill="${C.muted}">${num}</text>`);
    body.push(
      `<g><title>${esc(`${title}\n${second}`)}</title>` +
        `<rect x="${BOX_X}" y="${y}" width="${BOX_W}" height="${h}" rx="8" fill="${C.box}" stroke="${C.rule}" stroke-width="1.2"/>` +
        `<rect x="${BOX_X}" y="${y}" width="6" height="${h}" rx="3" fill="${accent(s)}"/>` +
        `<text x="${BOX_X + 18}" y="${y + 24}" font-size="13.5" font-weight="600" fill="${C.ink}">${esc(fit(title, BOX_W - 120, 13.5))}</text>` +
        `<text x="${BOX_X + BOX_W - 12}" y="${y + 24}" text-anchor="end" font-size="10.5" fill="${accent(s)}">${esc(STEP_TYPE_LABEL[s.type])}</text>` +
        `<text x="${BOX_X + 18}" y="${y + 44}" font-size="12" fill="${C.muted}">${esc(fit(second, BOX_W - 30, 12))}</text>` +
        `</g>`
    );

    const f = failure(s);
    if (f) {
      const my = y + h / 2;
      const [stroke, bg, text, dash] =
        f.kind === "stop"
          ? [C.fail, C.failBg, "✖ Fails: run stops", ""]
          : f.kind === "retry"
            ? [C.retry, C.retryBg, `↻ Retry ×${f.times}, then carry on`, ""]
            : [C.carry, C.carryBg, "Fails: carry on", ' stroke-dasharray="4 3"'];
      body.push(`<line x1="${BOX_X + BOX_W}" y1="${my}" x2="${BRANCH_X - 2}" y2="${my}" stroke="${stroke}" stroke-width="1.4"${dash} marker-end="url(#arrow-${f.kind})"/>`);
      body.push(pill(BRANCH_X, my - 13, BRANCH_W, 26, bg, stroke, fit(text, BRANCH_W - 12, 11), stroke, 11));
    }
    y += h;
  });

  // End.
  body.push(arrowDown(y, y + GAP));
  y += GAP;
  closeBand(y - GAP + 12);
  body.push(pill(CX - 70, y, 140, 32, C.readyBg, C.ready, "✔ Room ready", C.ready, 13, true));
  y += 32;
  body.push(`<text x="${CX}" y="${y + 18}" text-anchor="middle" font-size="11" fill="${C.muted}">Ready only if every step passes</text>`);
  y += 34;

  const marker = (id: string, color: string) =>
    `<marker id="${id}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="${color}"/></marker>`;

  return (
    `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${y}" viewBox="0 0 ${W} ${y}" font-family="${FONT}">` +
    `<defs>${marker("arrow", C.line)}${marker("arrow-stop", C.fail)}${marker("arrow-retry", C.retry)}${marker("arrow-carry", C.carry)}</defs>` +
    `<rect width="${W}" height="${y}" fill="#ffffff"/>` +
    bands.join("") +
    body.join("") +
    `</svg>`
  );
}

function slug(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "routine";
}

function save(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export function downloadDiagramSVG(svg: string, title: string) {
  save(new Blob([svg], { type: "image/svg+xml" }), `routine-${slug(title)}.svg`);
}

// downloadDiagramPNG renders the SVG at twice its size, for sharp images
// in documents and slides.
export async function downloadDiagramPNG(svg: string, title: string) {
  const url = URL.createObjectURL(new Blob([svg], { type: "image/svg+xml" }));
  try {
    const img = new Image();
    await new Promise<void>((resolve, reject) => {
      img.onload = () => resolve();
      img.onerror = () => reject(new Error("couldn't draw the diagram"));
      img.src = url;
    });
    const scale = 2;
    const canvas = document.createElement("canvas");
    canvas.width = img.width * scale;
    canvas.height = img.height * scale;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("couldn't draw the diagram");
    ctx.scale(scale, scale);
    ctx.drawImage(img, 0, 0);
    const blob = await new Promise<Blob | null>((r) => canvas.toBlob(r, "image/png"));
    if (!blob) throw new Error("couldn't make the PNG");
    save(blob, `routine-${slug(title)}.png`);
  } finally {
    URL.revokeObjectURL(url);
  }
}
