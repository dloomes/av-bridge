"use client";

import { useState } from "react";
import { Gauge, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { COMMAND_NAME, normaliseCommandName } from "@/lib/command-template";
import { InstanceTagList } from "@/components/command-editor";
import type { Subscription } from "@/lib/types";

// One editable row. `key` is a stable React key only; `rate` is carried
// through untouched so existing push subscriptions keep their tuned rate.
export interface SubscriptionRow {
  key: number;
  label: string;
  tag: string;
  attribute: string;
  channel: string;
  mode: "poll" | "subscribe";
  rate?: number;
}

let nextKey = 1;

function row(p: Partial<Omit<SubscriptionRow, "key">> = {}): SubscriptionRow {
  return {
    key: nextKey++,
    label: p.label ?? "",
    tag: p.tag ?? "",
    attribute: p.attribute ?? "",
    channel: p.channel ?? "1",
    mode: p.mode ?? "poll",
    rate: p.rate,
  };
}

export function rowsFromSubscriptions(subs?: Subscription[]): SubscriptionRow[] {
  return (subs ?? []).map((s) =>
    row({
      label: s.label,
      tag: s.tag,
      attribute: s.attribute,
      channel: String(s.channel || 1),
      // Historic rows have no mode — the bridge treats that as push.
      mode: s.mode === "poll" ? "poll" : "subscribe",
      rate: s.rate,
    })
  );
}

// Rows -> the device's subscriptions array. Fully blank rows are ignored;
// anything the Tesira adapter couldn't turn into a valid TTP call is an
// error the form shows before saving.
export function subscriptionsFromRows(rows: SubscriptionRow[]): Subscription[] {
  const out: Subscription[] = [];
  const labels = new Set<string>();
  for (const r of rows) {
    const label = r.label.trim();
    const tag = r.tag.trim();
    const attribute = r.attribute.trim();
    if (!label && !tag && !attribute) continue;
    const name = label || `${tag} ${attribute}`.trim();
    if (!tag) throw new Error(`Readings: "${name}" needs an instance tag.`);
    if (/\s/.test(tag)) {
      throw new Error(`Readings: instance tag "${tag}" can't contain spaces.`);
    }
    if (!attribute) throw new Error(`Readings: "${name}" needs an attribute (e.g. level).`);
    if (/\s/.test(attribute)) {
      throw new Error(`Readings: attribute "${attribute}" can't contain spaces.`);
    }
    if (!/^\d+$/.test(r.channel.trim()) || Number(r.channel) < 1) {
      throw new Error(`Readings: "${name}" needs a channel of 1 or higher.`);
    }
    if (!label) throw new Error(`Readings: ${tag} ${attribute} needs a label.`);
    if (!COMMAND_NAME.test(label)) {
      throw new Error(`Readings: label "${label}" can only use letters, numbers, - and _.`);
    }
    if (labels.has(label)) throw new Error(`Readings: label "${label}" is used twice.`);
    labels.add(label);
    const sub: Subscription = { tag, attribute, channel: Number(r.channel), label };
    if (r.mode === "poll") sub.mode = "poll";
    if (r.rate) sub.rate = r.rate;
    out.push(sub);
  }
  return out;
}

// "1-8", "1,3,5", "1-4, 7" -> [1..]. Returns null when unparseable.
const MAX_CHANNELS = 64;
function parseChannels(raw: string): number[] | null {
  const out = new Set<number>();
  for (const part of raw.split(",").map((p) => p.trim()).filter(Boolean)) {
    const m = part.match(/^(\d+)(?:\s*-\s*(\d+))?$/);
    if (!m) return null;
    const a = Number(m[1]);
    const b = m[2] ? Number(m[2]) : a;
    if (a < 1 || b < a || b - a >= MAX_CHANNELS) return null;
    for (let c = a; c <= b; c++) out.add(c);
  }
  const list = Array.from(out).sort((x, y) => x - y);
  return list.length > 0 && list.length <= MAX_CHANNELS ? list : null;
}

const labelClass =
  "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
const GRID =
  "grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_2.25rem] sm:grid-cols-[minmax(0,1.3fr)_minmax(0,1.2fr)_minmax(0,0.8fr)_3.5rem_minmax(0,1fr)_2.25rem] gap-2";

interface Props {
  rows: SubscriptionRow[];
  onChange: (rows: SubscriptionRow[]) => void;
  // The DSP's instance tags (empty until its Collector has connected);
  // undefined for devices that aren't Tesiras.
  instanceTags?: string[];
}

export function SubscriptionEditor({ rows, onChange, instanceTags }: Props) {
  const update = (key: number, patch: Partial<SubscriptionRow>) =>
    onChange(rows.map((r) => (r.key === key ? { ...r, ...patch } : r)));

  const addRows = (added: SubscriptionRow[]) => {
    const kept = rows.filter((r) => r.label.trim() || r.tag.trim() || r.attribute.trim());
    onChange([...kept, ...added]);
  };

  return (
    <div className="space-y-3">
      {rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          No readings yet. Each reading appears on the device page and can be
          checked in Room Readiness routines.
        </p>
      ) : (
        <div className="space-y-2">
          <div className={`hidden sm:grid ${GRID.replace("grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_2.25rem] ", "")}`}>
            <span className={labelClass}>Label</span>
            <span className={labelClass}>Instance tag</span>
            <span className={labelClass}>Attribute</span>
            <span className={labelClass}>Ch</span>
            <span className={labelClass}>Update</span>
            <span />
          </div>
          {rows.map((r) => {
            const meterOnPush = r.mode === "subscribe" && /meter/i.test(r.tag);
            return (
              <div key={r.key} className="space-y-1 rounded-md sm:rounded-none border sm:border-0 p-2 sm:p-0">
                <div className={GRID}>
                  <input
                    aria-label="Label"
                    className={`${inputClass} col-span-2 sm:col-span-1`}
                    value={r.label}
                    onChange={(e) => update(r.key, { label: normaliseCommandName(e.target.value) })}
                    placeholder="mic1_level_db"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <input
                    aria-label="Instance tag"
                    list="tesira-reading-tags"
                    className={`${inputClass} font-mono text-xs`}
                    value={r.tag}
                    onChange={(e) => update(r.key, { tag: e.target.value })}
                    placeholder="MicMeter1"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <input
                    aria-label="Attribute"
                    className={`${inputClass} font-mono text-xs col-span-2 sm:col-span-1`}
                    value={r.attribute}
                    onChange={(e) => update(r.key, { attribute: e.target.value })}
                    placeholder="level"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <input
                    aria-label="Channel"
                    className={inputClass}
                    inputMode="numeric"
                    value={r.channel}
                    onChange={(e) => update(r.key, { channel: e.target.value.trim() })}
                  />
                  <select
                    aria-label="Update"
                    className={`${inputClass} col-span-2 sm:col-span-1`}
                    value={r.mode}
                    onChange={(e) => update(r.key, { mode: e.target.value as SubscriptionRow["mode"] })}
                  >
                    <option value="poll">Snapshot</option>
                    <option value="subscribe">Live</option>
                  </select>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="col-start-3 row-start-1 sm:col-start-auto sm:row-start-auto"
                    aria-label={`Remove ${r.label || "reading"}`}
                    onClick={() => onChange(rows.filter((x) => x.key !== r.key))}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
                {meterOnPush && (
                  <p className="text-[11px] [color:hsl(var(--warning))]">
                    Meters change constantly — use Snapshot so every change isn’t recorded as an event.
                  </p>
                )}
              </div>
            );
          })}
        </div>
      )}

      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" onClick={() => addRows([row()])}>
          <Plus className="h-3.5 w-3.5" />
          Add reading
        </Button>
      </div>

      <MeterHelper onAdd={addRows} taken={new Set(rows.map((r) => r.label.trim()))} />
      {instanceTags && <InstanceTagList id="tesira-reading-tags" tags={instanceTags} />}

      <p className="text-[11px] text-muted-foreground">
        <strong>Snapshot</strong> reads the value on every poll, which suits meters.{" "}
        <strong>Live</strong> updates the moment the value changes and records each change
        as an event, which suits mute or call state.
      </p>
    </div>
  );
}

// Tesira DSP blocks the helper can add readings for, from the attributes
// Biamp's TTP reference documents. Meters change constantly, so they're
// snapshots; states are live, so each change is recorded as it happens.
type ReadingBlock = "meter" | "level" | "mute" | "logic" | "voip";

const READING_BLOCKS: { value: ReadingBlock; label: string; channels: boolean; hint: string }[] = [
  { value: "meter", label: "Meter", channels: true,
    hint: "One snapshot reading per channel, in dB, from an audio or peak meter." },
  { value: "level", label: "Level", channels: true,
    hint: "Each channel's level (dB) and mute, updated live." },
  { value: "mute", label: "Mute", channels: true,
    hint: "Each channel's mute, updated live: true when muted." },
  { value: "logic", label: "Logic meter", channels: true,
    hint: "Each channel's logic state, updated live: true when on." },
  { value: "voip", label: "VoIP call state", channels: false,
    hint: "The phone's call state, updated live, plus a count of active calls for Room Readiness checks." },
];

function blockRows(block: ReadingBlock, tag: string, prefix: string, list: number[]): SubscriptionRow[] {
  const ch = (c: number) => (list.length === 1 ? "" : `_ch${c}`);
  switch (block) {
    case "meter":
      return list.map((c) => row({ label: `${prefix}_ch${c}`, tag, attribute: "level", channel: String(c), mode: "poll" }));
    case "level":
      return list.flatMap((c) => [
        row({ label: `${prefix}${ch(c)}_level`, tag, attribute: "level", channel: String(c), mode: "subscribe" }),
        row({ label: `${prefix}${ch(c)}_mute`, tag, attribute: "mute", channel: String(c), mode: "subscribe" }),
      ]);
    case "mute":
      return list.map((c) => row({ label: `${prefix}${ch(c)}_mute`, tag, attribute: "mute", channel: String(c), mode: "subscribe" }));
    case "logic":
      return list.map((c) => row({ label: `${prefix}${ch(c)}_state`, tag, attribute: "state", channel: String(c), mode: "subscribe" }));
    case "voip":
      return [row({ label: `${prefix}_call`, tag, attribute: "callState", channel: "1", mode: "subscribe" })];
  }
}

// Adds the standard readings for one DSP block across a range of channels.
function MeterHelper({
  onAdd,
  taken,
}: {
  onAdd: (rows: SubscriptionRow[]) => void;
  taken: Set<string>;
}) {
  const [block, setBlock] = useState<ReadingBlock>("meter");
  const [tag, setTag] = useState("");
  const [channels, setChannels] = useState("1");
  const [prefix, setPrefix] = useState("");
  const [error, setError] = useState<string | null>(null);
  const spec = READING_BLOCKS.find((b) => b.value === block)!;

  const suggested = normaliseCommandName(tag.trim()).toLowerCase();
  const p = normaliseCommandName(prefix.trim()) || suggested || "block";
  const preview = blockRows(block, "x", p, spec.channels ? parseChannels(channels) ?? [1] : [1]).map((r) => r.label);

  const add = () => {
    const t = tag.trim();
    if (!t) return setError("Enter the block's instance tag from Tesira software.");
    if (/\s/.test(t)) return setError("Instance tags with spaces aren't supported.");
    const list = spec.channels ? parseChannels(channels) : [1];
    if (!list) return setError(`Channels: use a number, a range like 1-8, or a list like 1,3,5 (up to ${MAX_CHANNELS}).`);
    const added = blockRows(block, t, p, list);
    const clash = added.find((r) => taken.has(r.label));
    if (clash) return setError(`A reading called "${clash.label}" already exists — change the label prefix.`);
    setError(null);
    onAdd(added);
    setTag("");
    setChannels("1");
    setPrefix("");
  };

  return (
    <div className="rounded-md border border-dashed p-3 space-y-2">
      <div className="flex items-center gap-2 text-xs font-medium text-foreground">
        <Gauge className="h-3.5 w-3.5 text-primary" />
        Add readings for a DSP block
      </div>
      <div className="grid grid-cols-2 sm:grid-cols-[9rem_minmax(0,1fr)_6rem_minmax(0,1fr)_auto] gap-2 items-end">
        <div className="col-span-2 sm:col-span-1 space-y-1">
          <label htmlFor="reading-block" className={labelClass}>Block</label>
          <select
            id="reading-block"
            className={inputClass}
            value={block}
            onChange={(e) => { setBlock(e.target.value as ReadingBlock); setError(null); }}
          >
            {READING_BLOCKS.map((b) => (
              <option key={b.value} value={b.value}>{b.label}</option>
            ))}
          </select>
        </div>
        <div className="col-span-2 sm:col-span-1 space-y-1">
          <label htmlFor="meter-tag" className={labelClass}>Instance tag</label>
          <input
            id="meter-tag"
            list="tesira-reading-tags"
            className={`${inputClass} font-mono text-xs`}
            value={tag}
            onChange={(e) => { setTag(e.target.value); setError(null); }}
            placeholder={block === "voip" ? "VoIPControlStatus1" : block === "meter" ? "MicMeter1" : "Zone1Level"}
            spellCheck={false}
            autoComplete="off"
          />
        </div>
        {spec.channels ? (
          <div className="space-y-1">
            <label htmlFor="meter-channels" className={labelClass}>Channels</label>
            <input
              id="meter-channels"
              className={inputClass}
              value={channels}
              onChange={(e) => { setChannels(e.target.value); setError(null); }}
              placeholder="1-8"
            />
          </div>
        ) : (
          <div className="hidden sm:block" />
        )}
        <div className="space-y-1">
          <label htmlFor="meter-prefix" className={labelClass}>Label prefix</label>
          <input
            id="meter-prefix"
            className={inputClass}
            value={prefix}
            onChange={(e) => { setPrefix(e.target.value); setError(null); }}
            placeholder={suggested || "mic_meter"}
            spellCheck={false}
            autoComplete="off"
          />
        </div>
        <Button type="button" size="sm" className="col-span-2 sm:col-span-1 h-9" onClick={add}>
          Add
        </Button>
      </div>
      <p className="text-[11px] text-muted-foreground">
        {spec.hint} Labelled like <code>{preview.slice(0, 2).join(", ")}</code>
        {preview.length > 2 ? "…" : ""}.
      </p>
      {error && <p className="text-xs [color:hsl(var(--destructive))]">{error}</p>}
    </div>
  );
}
