"use client";

import { useState } from "react";
import { Plus, Trash2, Wand2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  COMMAND_NAME,
  normaliseCommandName,
  templatePlaceholders,
} from "@/lib/command-template";

// One editable row. `key` is a stable React key only — never persisted.
export interface CommandRow {
  key: number;
  name: string;
  command: string;
}

let nextKey = 1;

function row(name = "", command = ""): CommandRow {
  return { key: nextKey++, name, command };
}

export function rowsFromCommands(commands?: Record<string, string>): CommandRow[] {
  return Object.entries(commands ?? {}).map(([name, command]) => row(name, command));
}

// Turns the rows back into the device's commands map. Fully blank rows are
// ignored; anything half-filled, duplicated or oddly named is an error the
// form shows before saving.
export function commandsFromRows(rows: CommandRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const name = r.name.trim();
    const command = r.command.trim();
    if (!name && !command) continue;
    if (!name) throw new Error(`Commands: "${command}" needs a name.`);
    if (!COMMAND_NAME.test(name)) {
      throw new Error(`Commands: "${name}" can only use letters, numbers, - and _.`);
    }
    if (!command) throw new Error(`Commands: "${name}" needs a command string.`);
    if (name in out) throw new Error(`Commands: "${name}" is listed twice.`);
    out[name] = command;
  }
  return out;
}

// Standard Tesira block commands, generated from the block's instance tag.
// TTP form: <instance tag> <verb> <attribute> <channel> [value].
function tesiraBlockCommands(tag: string, channel: string, step: string): [string, string][] {
  return [
    ["mute", `${tag} set mute ${channel} true`],
    ["unmute", `${tag} set mute ${channel} false`],
    ["toggle_mute", `${tag} toggle mute ${channel}`],
    ["vol_up", `${tag} increment level ${channel} ${step}`],
    ["vol_down", `${tag} decrement level ${channel} ${step}`],
    ["set_level", `${tag} set level ${channel} {level}`],
  ];
}

const labelClass =
  "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

interface Props {
  rows: CommandRow[];
  onChange: (rows: CommandRow[]) => void;
  protocol: string;
}

export function CommandEditor({ rows, onChange, protocol }: Props) {
  const isTesira = protocol === "tesira";

  const update = (key: number, patch: Partial<CommandRow>) =>
    onChange(rows.map((r) => (r.key === key ? { ...r, ...patch } : r)));

  // Append generated commands. When a name is already taken (e.g. a second
  // block's "mute"), prefix it with the block tag so both survive.
  const addGenerated = (pairs: [string, string][], prefix?: string) => {
    const taken = new Set(rows.map((r) => r.name.trim()).filter(Boolean));
    const added: CommandRow[] = [];
    for (const [base, command] of pairs) {
      let name = base;
      if (taken.has(name) && prefix) name = normaliseCommandName(`${prefix}_${base}`);
      if (taken.has(name)) continue; // identical name already present — keep the operator's
      taken.add(name);
      added.push(row(name, command));
    }
    // Drop a lone blank starter row so generated commands don't sit under it.
    const kept = rows.filter((r) => r.name.trim() || r.command.trim());
    onChange([...kept, ...added]);
  };

  return (
    <div className="space-y-3">
      {rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          No commands yet. Each command becomes a button on the device page and
          can be used in Room Readiness routines.
        </p>
      ) : (
        <div className="space-y-2">
          <div className="hidden sm:grid sm:grid-cols-[minmax(0,11rem)_minmax(0,1fr)_2.25rem] gap-2">
            <span className={labelClass}>Button name</span>
            <span className={labelClass}>{isTesira ? "TTP command" : "Command"}</span>
            <span />
          </div>
          {rows.map((r) => {
            const asks = templatePlaceholders(r.command);
            return (
              <div key={r.key} className="space-y-1">
                <div className="grid grid-cols-[minmax(0,1fr)_2.25rem] sm:grid-cols-[minmax(0,11rem)_minmax(0,1fr)_2.25rem] gap-2">
                  <input
                    aria-label="Button name"
                    className={inputClass}
                    value={r.name}
                    onChange={(e) => update(r.key, { name: normaliseCommandName(e.target.value) })}
                    placeholder="mute"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <input
                    aria-label="Command"
                    className={`${inputClass} font-mono text-xs col-span-2 sm:col-span-1 order-3 sm:order-none`}
                    value={r.command}
                    onChange={(e) => update(r.key, { command: e.target.value })}
                    placeholder={isTesira ? "master_level set mute 1 true" : "command to send"}
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={`Remove ${r.name || "command"}`}
                    onClick={() => onChange(rows.filter((x) => x.key !== r.key))}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
                {asks.length > 0 && (
                  <p className="text-[11px] text-muted-foreground sm:pl-[calc(11rem+0.5rem)]">
                    Asks for {asks.map((a) => `“${a}”`).join(", ")} when pressed.
                  </p>
                )}
              </div>
            );
          })}
        </div>
      )}

      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" onClick={() => onChange([...rows, row()])}>
          <Plus className="h-3.5 w-3.5" />
          Add command
        </Button>
        {isTesira && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => addGenerated([["recall_preset", "DEVICE recallPreset {preset}"]])}
          >
            <Plus className="h-3.5 w-3.5" />
            Add preset recall
          </Button>
        )}
      </div>

      {isTesira && <TesiraBlockHelper onAdd={addGenerated} />}

      <p className="text-[11px] text-muted-foreground">
        Put <code className="rounded bg-muted px-1">{"{name}"}</code> in a command to ask for a
        value when the button is pressed, e.g.{" "}
        <code className="rounded bg-muted px-1">master_level set level 1 {"{level}"}</code>.
      </p>
    </div>
  );
}

// Generates the standard mute / volume / level commands for one Tesira
// block. The instance tag is the block name from Tesira software.
function TesiraBlockHelper({
  onAdd,
}: {
  onAdd: (pairs: [string, string][], prefix?: string) => void;
}) {
  const [tag, setTag] = useState("");
  const [channel, setChannel] = useState("1");
  const [step, setStep] = useState("3");
  const [error, setError] = useState<string | null>(null);

  const add = () => {
    const t = tag.trim();
    if (!t) return setError("Enter the block's instance tag from Tesira software.");
    if (/\s/.test(t)) {
      return setError("Instance tags with spaces aren't supported here — add those commands by hand.");
    }
    if (!/^\d+$/.test(channel) || Number(channel) < 1) return setError("Channel must be 1 or higher.");
    if (!/^\d+(\.\d+)?$/.test(step) || Number(step) <= 0) return setError("Step must be a positive number of dB.");
    setError(null);
    onAdd(tesiraBlockCommands(t, channel, step), t);
    setTag("");
  };

  return (
    <div className="rounded-md border border-dashed p-3 space-y-2">
      <div className="flex items-center gap-2 text-xs font-medium text-foreground">
        <Wand2 className="h-3.5 w-3.5 text-primary" />
        Add standard commands for a level or mute block
      </div>
      <div className="grid grid-cols-2 sm:grid-cols-[minmax(0,1fr)_5rem_5rem_auto] gap-2 items-end">
        <div className="col-span-2 sm:col-span-1 space-y-1">
          <label htmlFor="tesira-tag" className={labelClass}>Instance tag</label>
          <input
            id="tesira-tag"
            className={`${inputClass} font-mono text-xs`}
            value={tag}
            onChange={(e) => { setTag(e.target.value); setError(null); }}
            onKeyDown={(e) => {
              if (e.key === "Enter") { e.preventDefault(); add(); }
            }}
            placeholder="master_level"
            spellCheck={false}
            autoComplete="off"
          />
        </div>
        <div className="space-y-1">
          <label htmlFor="tesira-channel" className={labelClass}>Channel</label>
          <input
            id="tesira-channel"
            className={inputClass}
            inputMode="numeric"
            value={channel}
            onChange={(e) => { setChannel(e.target.value.trim()); setError(null); }}
          />
        </div>
        <div className="space-y-1">
          <label htmlFor="tesira-step" className={labelClass}>Step (dB)</label>
          <input
            id="tesira-step"
            className={inputClass}
            inputMode="decimal"
            value={step}
            onChange={(e) => { setStep(e.target.value.trim()); setError(null); }}
          />
        </div>
        <Button type="button" size="sm" className="col-span-2 sm:col-span-1 h-9" onClick={add}>
          Add
        </Button>
      </div>
      <p className="text-[11px] text-muted-foreground">
        Adds mute, unmute, toggle mute, volume up/down and set level. If a name is already
        used, the new one is prefixed with the tag (e.g. <code>zone2_mute</code>).
      </p>
      {error && (
        <p className="text-xs [color:hsl(var(--destructive))]">{error}</p>
      )}
    </div>
  );
}
