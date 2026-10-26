// Room Readiness run report as a PDF, made in the browser. jsPDF is only
// loaded when a report is asked for, so it adds nothing to page loads.

import type { NightlyRunDetail, NightlyStepRow } from "@/lib/api";
import { readingLabel } from "@/lib/api";
import { summariseActual, summariseExpected } from "@/lib/step-summary";
import { PRODUCT_NAME } from "@/lib/brand";

const KIND: Record<string, string> = {
  power: "Overnight power cycle",
  readiness: "Readiness check",
};

const STATUS: Record<string, { label: string; rgb: [number, number, number] }> = {
  succeeded: { label: "READY", rgb: [22, 128, 61] },
  failed: { label: "FAILED", rgb: [190, 30, 45] },
  in_progress: { label: "IN PROGRESS", rgb: [90, 100, 115] },
  pending: { label: "PENDING", rgb: [90, 100, 115] },
  skipped: { label: "SKIPPED", rgb: [90, 100, 115] },
};

const INK: [number, number, number] = [24, 28, 36];
const MUTED: [number, number, number] = [100, 110, 125];
const RULE: [number, number, number] = [220, 224, 230];

// pdfText keeps text to what the PDF's built-in font can draw: accents
// are folded ("Café" → "Cafe"), typographic punctuation made plain.
function pdfText(s: string | undefined | null): string {
  if (!s) return "";
  return s
    .replace(/[–—]/g, "-")
    .replace(/[‘’]/g, "'")
    .replace(/[“”]/g, '"')
    .replace(/…/g, "...")
    .replace(/≤/g, "<=")
    .replace(/≥/g, ">=")
    .replace(/·/g, "-")
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^\x20-\x7e -ÿ\n]/g, "?");
}

function when(iso?: string): string {
  if (!iso) return "-";
  return new Date(iso).toLocaleString(undefined, {
    weekday: "short",
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function duration(seconds?: number): string {
  if (seconds === undefined) return "-";
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s === 0 ? `${m}m` : `${m}m ${s}s`;
}

// value shows an expected/actual value. A check ({op, value}) reads as
// "> -40" rather than its JSON.
function value(v: unknown): string {
  if (v === undefined || v === null) return "-";
  if (typeof v === "object" && !Array.isArray(v) && "value" in (v as object)) {
    const c = v as { op?: string; value?: unknown };
    const op = c.op === "==" ? "=" : c.op ?? "=";
    return `${op} ${value(c.value)}`;
  }
  const s = typeof v === "string" ? v : JSON.stringify(v);
  return s.length > 120 ? s.slice(0, 117) + "..." : s;
}

// actualWithReadings is the Actual cell: what the step found, a line per
// device, and where each reading came from ("Display: offline - Fresh poll").
function actualWithReadings(s: NightlyStepRow): string {
  if (s.actual === undefined && !(s.readings?.length)) return "-";
  return summariseActual(s)
    .map((l) => (l.device ? `${l.device}: ` : "") + l.value + (l.reading ? ` - ${readingLabel(l.reading)}` : ""))
    .join("\n");
}

function slug(s: string): string {
  return pdfText(s).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "room";
}

export async function downloadRunReport(run: NightlyRunDetail, orgName?: string) {
  const [{ jsPDF }, autoTableMod] = await Promise.all([import("jspdf"), import("jspdf-autotable")]);
  const autoTable = autoTableMod.autoTable ?? autoTableMod.default;

  const doc = new jsPDF({ unit: "mm", format: "a4" });
  const W = doc.internal.pageSize.getWidth();
  const M = 16; // margin
  let y = M;

  // ── Heading ──
  doc.setFont("helvetica", "normal");
  doc.setFontSize(9);
  doc.setTextColor(...MUTED);
  doc.text(pdfText(orgName || PRODUCT_NAME), M, y);
  doc.text("Room Readiness report", W - M, y, { align: "right" });
  y += 9;

  doc.setFont("helvetica", "bold");
  doc.setFontSize(20);
  doc.setTextColor(...INK);
  doc.text(pdfText(run.room_name), M, y);
  y += 6;
  doc.setFont("helvetica", "normal");
  doc.setFontSize(10);
  doc.setTextColor(...MUTED);
  const place = [run.building_name, run.location_name, run.region_name].filter(Boolean).join(", ");
  doc.text(pdfText([KIND[run.kind] ?? run.kind, place].filter(Boolean).join("  -  ")), M, y);
  y += 8;

  // ── Result band ──
  const st = STATUS[run.status] ?? STATUS.pending;
  // A readiness check counts its routine's steps; a power cycle, its
  // power commands (recorded as steps before the routine's).
  const steps = run.kind === "power" ? run.steps : run.steps.filter((s) => s.step_index >= 0);
  const passed = steps.filter((s) => s.passed).length;
  const noun = run.kind === "power" ? "power command" : "step";
  const verb = run.kind === "power" ? "succeeded" : "passed";
  doc.setFillColor(...st.rgb);
  doc.roundedRect(M, y, W - 2 * M, 14, 2, 2, "F");
  doc.setTextColor(255, 255, 255);
  doc.setFont("helvetica", "bold");
  doc.setFontSize(13);
  doc.text(st.label, M + 5, y + 9.2);
  doc.setFont("helvetica", "normal");
  doc.setFontSize(10);
  const summary =
    steps.length > 0
      ? `${passed} of ${steps.length} ${noun}${steps.length === 1 ? "" : "s"} ${verb}`
      : run.kind === "power"
        ? "No power commands recorded"
        : "No steps recorded";
  doc.text(summary, W - M - 5, y + 9.2, { align: "right" });
  y += 20;

  // ── Facts ──
  autoTable(doc, {
    startY: y,
    margin: { left: M, right: M },
    theme: "plain",
    styles: { fontSize: 9.5, cellPadding: { top: 1.4, bottom: 1.4, left: 0, right: 4 }, textColor: INK },
    columnStyles: { 0: { cellWidth: 38, textColor: MUTED } },
    body: [
      ...(run.kind === "power" ? [] : [["Routine", pdfText(run.routine_name || "None assigned")]]),
      ["Scheduled", when(run.scheduled_at)],
      ["Started", when(run.started_at)],
      ...(run.kind === "power" ? [["Powered on", when(run.powered_on_at)]] : []),
      ["Finished", when(run.completed_at)],
      ["Duration", duration(run.duration_seconds)],
    ],
  });
  y = (doc as unknown as { lastAutoTable: { finalY: number } }).lastAutoTable.finalY + 4;

  if (run.failure_reason) {
    doc.setDrawColor(...STATUS.failed.rgb);
    doc.setFillColor(253, 240, 241);
    const lines = doc.splitTextToSize(pdfText(run.failure_reason), W - 2 * M - 10);
    const h = 9 + lines.length * 4.6;
    doc.roundedRect(M, y, W - 2 * M, h, 2, 2, "FD");
    doc.setTextColor(...STATUS.failed.rgb);
    doc.setFont("helvetica", "bold");
    doc.setFontSize(9);
    doc.text("Why it failed", M + 5, y + 5.5);
    doc.setFont("helvetica", "normal");
    doc.setFontSize(9.5);
    doc.text(lines, M + 5, y + 10.5);
    y += h + 6;
  }

  // ── Steps ──
  doc.setTextColor(...INK);
  doc.setFont("helvetica", "bold");
  doc.setFontSize(12);
  doc.text("Step results", M, y + 4);
  y += 7;
  if (run.steps.length === 0) {
    doc.setFont("helvetica", "normal");
    doc.setFontSize(9.5);
    doc.setTextColor(...MUTED);
    doc.text(
      run.kind === "power"
        ? "No power commands were recorded for this run."
        : "No step results were recorded for this run.",
      M,
      y + 4
    );
  } else {
    const row = (s: NightlyStepRow) => [
      s.step_index < 0 ? "-" : String(s.step_index + 1),
      pdfText(s.step_name),
      pdfText(s.device_name) || "-",
      pdfText(s.expected === undefined ? "-" : summariseExpected(s)),
      pdfText(actualWithReadings(s)),
      s.passed ? "Pass" : pdfText(s.error ? `Fail: ${s.error}` : "Fail"),
    ];
    autoTable(doc, {
      startY: y,
      margin: { left: M, right: M, bottom: 18 },
      head: [["#", "Step", "Device", "Expected", "Actual", "Result"]],
      body: run.steps.map(row),
      theme: "grid",
      styles: { fontSize: 8.5, cellPadding: 1.8, textColor: INK, lineColor: RULE, lineWidth: 0.2, overflow: "linebreak" },
      headStyles: { fillColor: [244, 246, 248], textColor: MUTED, fontStyle: "bold", fontSize: 8 },
      columnStyles: {
        0: { cellWidth: 8, halign: "right", textColor: MUTED },
        1: { cellWidth: 40, fontStyle: "bold" },
        2: { cellWidth: 30 },
        3: { cellWidth: 28 },
        4: { cellWidth: 28 },
      },
      didParseCell: (d) => {
        if (d.section !== "body") return;
        const s = run.steps[d.row.index];
        if (!s.passed) d.cell.styles.fillColor = [253, 240, 241];
        if (d.column.index === 5) {
          d.cell.styles.textColor = s.passed ? STATUS.succeeded.rgb : STATUS.failed.rgb;
          d.cell.styles.fontStyle = "bold";
        }
      },
    });
  }

  // ── Footer on every page ──
  const generated = when(new Date().toISOString());
  const pages = doc.getNumberOfPages();
  for (let p = 1; p <= pages; p++) {
    doc.setPage(p);
    const H = doc.internal.pageSize.getHeight();
    doc.setDrawColor(...RULE);
    doc.line(M, H - 12, W - M, H - 12);
    doc.setFont("helvetica", "normal");
    doc.setFontSize(7.5);
    doc.setTextColor(...MUTED);
    doc.text(pdfText(`Generated ${generated} by ${PRODUCT_NAME}  -  Run ${run.id}`), M, H - 7.5);
    doc.text(`Page ${p} of ${pages}`, W - M, H - 7.5, { align: "right" });
  }

  const day = (run.completed_at ?? run.scheduled_at).slice(0, 10);
  doc.save(`room-readiness-${slug(run.room_name)}-${day}.pdf`);
}
