// The product roadmap as a PDF, made in the browser. jsPDF is only loaded when
// a PDF is asked for, so it adds nothing to page loads. Everything drawn is
// plain text from the roadmap passed in; there are no links and no requests.

import type { Roadmap, RoadmapStatus } from "@/lib/types";

type RGB = [number, number, number];

const STATUS_LABEL: Record<RoadmapStatus, string> = {
  committed: "Committed",
  planned: "Planned",
  exploring: "Exploring",
};

const LEGEND: Record<RoadmapStatus, string> = {
  committed: "in a named monthly release",
  planned: "designed; the quarter may move",
  exploring: "shaping it with customers",
};

const INK: RGB = [24, 28, 36];
const MUTED: RGB = [100, 110, 125];
const FAINT: RGB = [130, 135, 145];
const RULE: RGB = [210, 215, 222];
// --primary (light mode) in globals.css: hsl(221 83% 53%).
const ACCENT: RGB = [36, 99, 235];
const NOW_TINT: RGB = [236, 242, 254];

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

// pdfText keeps text to what the PDF's built-in font can draw: accents
// are folded, typographic punctuation made plain.
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
    .replace(/[^\x20-\x7e\n -ÿ]/g, "?");
}

function formatAsOf(iso: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso);
  if (!m) return iso;
  const mon = MONTHS[Number(m[2]) - 1];
  if (!mon) return iso;
  return `${Number(m[3])} ${mon} ${m[1]}`;
}

// One drawn line of a cell. Cells are lists of these so a tall row can be
// split between pages without cutting text.
interface Line {
  text: string;
  bold?: boolean;
  size: number;
  rgb: RGB;
  h: number; // line height, mm
  pre: number; // space above, mm
  status?: RoadmapStatus; // meta line: draws a swatch first
  start?: boolean; // first line of an item: items stay together
}

export async function downloadRoadmapPdf(roadmap: Roadmap, opts?: { sharedWith?: string }): Promise<void> {
  const { jsPDF } = await import("jspdf");
  const { settings, periods, themes, items, shipped } = roadmap;
  const asOf = formatAsOf(settings.as_of);

  const doc = new jsPDF({ unit: "mm", format: "a4", orientation: "landscape" });
  const W = doc.internal.pageSize.getWidth();
  const H = doc.internal.pageSize.getHeight();
  const M = 12;
  const BOTTOM = H - 17; // room for the footer
  const PAD = 2;
  const LABEL_W = 38;
  const cols = Math.max(periods.length, 1);
  const colW = (W - 2 * M - LABEL_W) / cols;
  const HEAD_H = 11;
  let y = M;

  const font = (size: number, bold = false, rgb: RGB = INK) => {
    doc.setFont("helvetica", bold ? "bold" : "normal");
    doc.setFontSize(size);
    doc.setTextColor(...rgb);
  };
  const wrap = (text: string, width: number, size: number, bold = false): string[] => {
    doc.setFont("helvetica", bold ? "bold" : "normal");
    doc.setFontSize(size);
    return doc.splitTextToSize(pdfText(text), width) as string[];
  };

  const swatch = (status: RoadmapStatus, x: number, cy: number) => {
    const r = 1.4;
    doc.setLineWidth(0.4);
    if (status === "committed") {
      doc.setFillColor(...ACCENT);
      doc.circle(x + r, cy, r, "F");
    } else if (status === "planned") {
      doc.setDrawColor(...ACCENT);
      doc.setLineDashPattern([], 0);
      doc.circle(x + r, cy, r - 0.2, "S");
    } else {
      doc.setDrawColor(...MUTED);
      doc.setLineDashPattern([0.6, 0.6], 0);
      doc.circle(x + r, cy, r - 0.2, "S");
      doc.setLineDashPattern([], 0);
    }
    doc.setLineWidth(0.2);
  };

  // ── Header ──
  font(8.5, false, MUTED);
  doc.text(pdfText(`${settings.title} - as of ${asOf}`).toUpperCase(), M, y + 3);
  y += 9;
  font(20, true, ACCENT);
  const headLines = wrap(settings.headline, W - 2 * M, 20, true);
  doc.text(headLines, M, y + 5);
  y += headLines.length * 8.5 + 1;
  font(10.5, false, MUTED);
  const ledeLines = wrap(settings.lede, 200, 10.5);
  doc.text(ledeLines, M, y + 3);
  y += ledeLines.length * 4.8 + 3;
  font(9, false, INK);
  doc.text(`As of ${asOf}`, M, y + 3);
  y += 8;

  // ── Legend ──
  let lx = M;
  (Object.keys(STATUS_LABEL) as RoadmapStatus[]).forEach((s) => {
    swatch(s, lx, y + 1.6);
    font(8.5, true, INK);
    const label = STATUS_LABEL[s];
    doc.text(label, lx + 5, y + 2.6);
    const lw = doc.getTextWidth(label);
    font(8.5, false, MUTED);
    const desc = pdfText(LEGEND[s]);
    doc.text(desc, lx + 5 + lw + 1.5, y + 2.6);
    lx += 5 + lw + 1.5 + doc.getTextWidth(desc) + 10;
  });
  y += 9;

  // ── Grid ──
  const drawPeriodHeader = () => {
    // The theme column's header stays blank; each period has name + months.
    periods.forEach((p, i) => {
      const x = M + LABEL_W + i * colW;
      if (p.is_current) {
        doc.setFillColor(...ACCENT);
        doc.rect(x, y, colW, HEAD_H, "F");
      } else {
        doc.setFillColor(244, 246, 248);
        doc.rect(x, y, colW, HEAD_H, "F");
      }
      const fg: RGB = p.is_current ? [255, 255, 255] : INK;
      font(9.5, true, fg);
      const name = pdfText(p.name) + (p.is_current ? " (now)" : "");
      doc.text(doc.splitTextToSize(name, colW - 2 * PAD)[0] as string, x + PAD, y + 4.6);
      font(7.5, false, p.is_current ? [255, 255, 255] : MUTED);
      doc.text(doc.splitTextToSize(pdfText(p.months), colW - 2 * PAD)[0] as string, x + PAD, y + 8.6);
    });
    doc.setDrawColor(...RULE);
    doc.line(M, y + HEAD_H, W - M, y + HEAD_H);
    y += HEAD_H;
  };

  const newPage = () => {
    doc.addPage();
    y = M;
  };
  const ensure = (h: number) => {
    if (y + h > BOTTOM) newPage();
  };

  const cellLines = (themeId: string, position: number): Line[] => {
    const out: Line[] = [];
    items
      .filter((i) => i.theme_id === themeId && i.period === position)
      .forEach((i, idx) => {
        const pre = idx === 0 ? 0 : 3;
        wrap(i.title, colW - 2 * PAD, 9, true).forEach((t, k) =>
          out.push({ text: t, bold: true, size: 9, rgb: INK, h: 4, pre: k === 0 ? pre : 0, start: k === 0 })
        );
        const meta = STATUS_LABEL[i.status] ?? i.status;
        out.push({
          text: pdfText(meta + (i.release_month ? ` - ${i.release_month} release` : "")),
          size: 7.5,
          rgb: ACCENT,
          h: 3.6,
          pre: 0.4,
          status: i.status,
        });
        wrap(i.description, colW - 2 * PAD, 8).forEach((t, k) =>
          out.push({ text: t, size: 8, rgb: MUTED, h: 3.7, pre: k === 0 ? 0.6 : 0 })
        );
      });
    return out;
  };

  const labelLines = (name: string, note: string): Line[] => {
    const w = LABEL_W - PAD - 2;
    const out: Line[] = wrap(name, w, 9, true).map((t, k0) => ({ text: t, bold: true, size: 9, rgb: INK, h: 4, pre: 0, start: k0 === 0 }));
    wrap(note, w, 7.5).forEach((t, k) => out.push({ text: t, size: 7.5, rgb: MUTED, h: 3.4, pre: k === 0 ? 1 : 0 }));
    return out;
  };

  // How many lines of `lines` (from `from`) fit in `avail` mm. Items are
  // kept whole: one that doesn't fit is left for the next page, and is only
  // split when it is taller than a full page (`full`).
  const fit = (lines: Line[], from: number, avail: number, full = avail): { count: number; used: number } => {
    let used = 0;
    let k = from;
    while (k < lines.length) {
      let e = k + 1;
      while (e < lines.length && !lines[e].start) e++;
      let gh = 0;
      for (let j = k; j < e; j++) gh += lines[j].h + (j === k ? 0 : lines[j].pre);
      const need = gh + (k === from ? 0 : lines[k].pre);
      if (used + need <= avail) {
        used += need;
        k = e;
        continue;
      }
      if (gh > full) {
        // Taller than a page by itself: split by line.
        for (; k < e; k++) {
          const n = lines[k].h + (k === from ? 0 : lines[k].pre);
          if (used + n > avail) break;
          used += n;
        }
      }
      break;
    }
    return { count: k - from, used };
  };
  const total = (lines: Line[]) => fit(lines, 0, 1e9).used;
  const FULL = BOTTOM - M - HEAD_H - 2 * PAD;

  const drawLines = (lines: Line[], from: number, count: number, x: number, top: number) => {
    let cy = top;
    for (let k = from; k < from + count; k++) {
      const l = lines[k];
      if (k > from) cy += l.pre;
      font(l.size, !!l.bold, l.rgb);
      const base = cy + l.h - 1;
      if (l.status) {
        swatch(l.status, x, base - 1.1);
        doc.text(l.text, x + 4.5, base);
      } else {
        doc.text(l.text, x, base);
      }
      cy += l.h;
    }
  };

  // The grid starts with its header. Keep it with at least a few lines of
  // the first row, otherwise move it all to a fresh page.
  if (y + HEAD_H + 25 > BOTTOM) newPage();
  drawPeriodHeader();

  themes.forEach((t) => {
    const origLabel = labelLines(t.name, t.note);
    const contLabel = labelLines(`${t.name} (continued)`, "");
    let continued = false;
    const columns: Line[][] = [
      origLabel,
      ...periods.map((p) => cellLines(t.id, p.position)),
    ];
    const cursor = columns.map(() => 0);
    const rowH = Math.max(...columns.map(total)) + 2 * PAD;

    // Whole row fits here, or fits on a fresh page: no splitting.
    const fullPage = BOTTOM - M - HEAD_H;
    if (y + rowH > BOTTOM && rowH <= fullPage) {
      newPage();
      drawPeriodHeader();
    }

    // Otherwise draw in chunks, repeating the period header per page.
    for (;;) {
      const avail = BOTTOM - y - 2 * PAD;
      // On a continuation page the theme name is shown again.
      if (continued && (columns[0] === contLabel || cursor[0] >= origLabel.length)) {
        columns[0] = contLabel;
        cursor[0] = 0;
      }
      const chunk = columns.map((c, i) => fit(c, cursor[i], avail, FULL));
      const progressed = chunk.some((c) => c.count > 0);
      if (!progressed) {
        // Nothing fits (a few mm left): go to the next page and retry.
        newPage();
        drawPeriodHeader();
        continue;
      }
      const h = Math.max(...chunk.map((c) => c.used)) + 2 * PAD;
      periods.forEach((p, i) => {
        if (p.is_current) {
          doc.setFillColor(...NOW_TINT);
          doc.rect(M + LABEL_W + i * colW, y, colW, h, "F");
        }
      });
      columns.forEach((c, i) => {
        const x = i === 0 ? M : M + LABEL_W + (i - 1) * colW;
        drawLines(c, cursor[i], chunk[i].count, x + (i === 0 ? 0 : PAD), y + PAD);
        cursor[i] += chunk[i].count;
      });
      // Cell rules.
      doc.setDrawColor(...RULE);
      doc.setLineDashPattern([], 0);
      doc.line(M, y + h, W - M, y + h);
      for (let i = 0; i < cols; i++) doc.line(M + LABEL_W + i * colW, y, M + LABEL_W + i * colW, y + h);
      y += h;
      if (columns.every((c, i) => cursor[i] >= c.length)) break;
      continued = true;
      newPage();
      drawPeriodHeader();
    }
  });

  // ── Recently shipped ──
  if (shipped.length > 0) {
    y += 8;
    ensure(8 + 9);
    font(13, true, ACCENT);
    doc.text("Recently shipped", M, y + 4);
    y += 8;
    const WHEN_W = 26;
    shipped.forEach((s) => {
      const lines = wrap(s.title, W - 2 * M - WHEN_W, 9.5);
      const h = lines.length * 4.4 + 3;
      ensure(h);
      doc.setDrawColor(...RULE);
      doc.line(M, y, W - M, y);
      font(8.5, false, MUTED);
      doc.text(doc.splitTextToSize(pdfText(s.when), WHEN_W - 2) as string[], M, y + 4.2);
      font(9.5, false, INK);
      doc.text(lines, M + WHEN_W, y + 4.2);
      y += h;
    });
  }

  // ── Disclaimer ──
  if (settings.disclaimer) {
    y += 6;
    const lines = wrap(settings.disclaimer, 230, 8.5);
    // Keep it together when it can be, but never clip it.
    const hAll = lines.length * 3.9;
    if (y + hAll > BOTTOM && hAll < BOTTOM - M) newPage();
    font(8.5, false, MUTED);
    let i = 0;
    while (i < lines.length) {
      if (y + 3.9 > BOTTOM) newPage();
      doc.text(lines[i], M, y + 3);
      y += 3.9;
      i++;
    }
  }

  // ── Footer on every page ──
  const pages = doc.getNumberOfPages();
  for (let p = 1; p <= pages; p++) {
    doc.setPage(p);
    doc.setDrawColor(...RULE);
    doc.setLineDashPattern([], 0);
    doc.line(M, H - 14, W - M, H - 14);
    font(7.5, false, MUTED);
    doc.text(pdfText(`M.A.R.C.U.S. roadmap · as of ${asOf} · page ${p} of ${pages}`), M, H - 10);
    if (opts?.sharedWith !== undefined) {
      font(7.5, false, FAINT);
      doc.text(pdfText(`Shared with ${opts.sharedWith} by Involve. Please don't forward.`), M, H - 6);
    }
  }

  const iso = /^\d{4}-\d{2}-\d{2}$/.test(settings.as_of) ? settings.as_of : new Date().toISOString().slice(0, 10);
  doc.save(`marcus-roadmap-${iso}.pdf`);
}
