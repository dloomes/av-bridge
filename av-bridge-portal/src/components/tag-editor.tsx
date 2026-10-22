"use client";

import { Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";

// One editable tag. `key` is a stable React key only — never persisted.
export interface TagRow {
  key: number;
  name: string;
  value: string;
}

let nextKey = 1;

export function rowsFromTags(tags?: Record<string, string>): TagRow[] {
  return Object.entries(tags ?? {})
    .sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }))
    .map(([name, value]) => ({ key: nextKey++, name, value: String(value) }));
}

// tagsFromRows drops blank rows and refuses duplicate or spaced names, so
// the form can say what's wrong instead of saving something unexpected.
export function tagsFromRows(rows: TagRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const name = r.name.trim();
    const value = r.value.trim();
    if (!name && !value) continue;
    if (!name) throw new Error(`Tags: the tag with value "${value}" needs a name.`);
    if (/\s/.test(name)) throw new Error(`Tags: "${name}" can't contain spaces; use underscores, like outlet_1_name.`);
    if (name in out) throw new Error(`Tags: "${name}" is listed twice.`);
    out[name] = value;
  }
  return out;
}

const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
const labelClass = "text-xs font-medium text-muted-foreground uppercase tracking-wide";

// Hints for the protocols whose adapters read settings from tags.
const HINTS: Record<string, string> = {
  aten_pdu: "Label outlets with outlet_1_name, outlet_2_name … (the PDU can't report its own names over Telnet). Set outlet_count for PDUs with more than 8 outlets.",
  netgear_m4250: "For an M4350, set port_prefix to 1/0/.",
  cisco_roomos: "Set tls_skip_verify to true for a codec with a self-signed certificate.",
  cisco_tc: "Set tls_skip_verify to true for a codec with a self-signed certificate.",
  poly_videoos: "Set tls_skip_verify to true for a codec with a self-signed certificate.",
};

interface Props {
  rows: TagRow[];
  onChange: (rows: TagRow[]) => void;
  protocol: string;
}

export function TagEditor({ rows, onChange, protocol }: Props) {
  const update = (key: number, patch: Partial<TagRow>) =>
    onChange(rows.map((r) => (r.key === key ? { ...r, ...patch } : r)));
  const hint = HINTS[protocol];

  return (
    <div className="space-y-2">
      {rows.length > 0 && (
        <div className="hidden sm:grid grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)_2.25rem] gap-2">
          <span className={labelClass}>Tag</span>
          <span className={labelClass}>Value</span>
          <span />
        </div>
      )}
      {rows.map((r) => (
        <div key={r.key} className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)_2.25rem] gap-2">
          <input
            aria-label="Tag name"
            className={`${inputClass} font-mono text-xs`}
            value={r.name}
            onChange={(e) => update(r.key, { name: e.target.value })}
            placeholder="outlet_1_name"
            spellCheck={false}
            autoComplete="off"
          />
          <input
            aria-label={`Value for ${r.name || "tag"}`}
            className={inputClass}
            value={r.value}
            onChange={(e) => update(r.key, { value: e.target.value })}
            placeholder="Codec"
            autoComplete="off"
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label={`Remove ${r.name || "tag"}`}
            onClick={() => onChange(rows.filter((x) => x.key !== r.key))}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onChange([...rows, { key: nextKey++, name: "", value: "" }])}
      >
        <Plus className="h-3.5 w-3.5" />
        Add tag
      </Button>
      <p className="text-[11px] text-muted-foreground">
        Tags are settings some adapters read, and labels for your own use.{hint ? ` ${hint}` : ""}
      </p>
    </div>
  );
}
