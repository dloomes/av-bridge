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

// Tesira DSP block types the helper can generate commands for. TTP form:
// <instance tag> <verb> <attribute> [<index>] [value]. Level and mute
// strings follow Biamp's TTP reference; the others follow Biamp's usual
// attribute names, so test each from the device page.
type TesiraBlock = "level" | "mute" | "logic" | "source" | "voip";

const TESIRA_BLOCKS: { value: TesiraBlock; label: string; channel: boolean; hint: string }[] = [
  { value: "level", label: "Level", channel: true,
    hint: "Adds mute, unmute, toggle mute, volume up and down, and set level." },
  { value: "mute", label: "Mute", channel: true,
    hint: "Adds mute, unmute and toggle mute." },
  { value: "logic", label: "Logic state", channel: true,
    hint: "Adds on, off and toggle, for a Logic State block driving room logic." },
  { value: "source", label: "Source selector", channel: false,
    hint: "Adds select source, which asks for the source number (0 selects none)." },
  { value: "voip", label: "VoIP phone", channel: false,
    hint: "Adds dial (asks for the number), answer and hang up, on the line you choose." },
];

function tesiraBlockCommands(
  block: TesiraBlock,
  tag: string,
  channel: string,
  step: string,
  line: string,
): [string, string][] {
  switch (block) {
    case "level":
      return [
        ["mute", `${tag} set mute ${channel} true`],
        ["unmute", `${tag} set mute ${channel} false`],
        ["toggle_mute", `${tag} toggle mute ${channel}`],
        ["vol_up", `${tag} increment level ${channel} ${step}`],
        ["vol_down", `${tag} decrement level ${channel} ${step}`],
        ["set_level", `${tag} set level ${channel} {level}`],
      ];
    case "mute":
      return [
        ["mute", `${tag} set mute ${channel} true`],
        ["unmute", `${tag} set mute ${channel} false`],
        ["toggle_mute", `${tag} toggle mute ${channel}`],
      ];
    case "logic":
      return [
        ["logic_on", `${tag} set state ${channel} true`],
        ["logic_off", `${tag} set state ${channel} false`],
        ["logic_toggle", `${tag} toggle state ${channel}`],
      ];
    case "source":
      return [["select_source", `${tag} set sourceSelection {source}`]];
    case "voip":
      return [
        ["dial", `${tag} dial ${line} 1 {number}`],
        ["answer", `${tag} answer ${line} 1`],
        ["hang_up", `${tag} end ${line} 1`],
      ];
  }
}

const labelClass =
  "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

interface Props {
  rows: CommandRow[];
  onChange: (rows: CommandRow[]) => void;
  protocol: string;
  // The DSP's instance tags, once its Collector has connected to it.
  instanceTags?: string[];
}

export function CommandEditor({ rows, onChange, protocol, instanceTags }: Props) {
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

      {isTesira && <TesiraBlockHelper onAdd={addGenerated} instanceTags={instanceTags} />}

      <p className="text-[11px] text-muted-foreground">
        Put <code className="rounded bg-muted px-1">{"{name}"}</code> in a command to ask for a
        value when the button is pressed, e.g.{" "}
        <code className="rounded bg-muted px-1">master_level set level 1 {"{level}"}</code>.
      </p>
    </div>
  );
}

// Generates the standard commands for one Tesira DSP block. The instance
// tag is the block name from Tesira software.
function TesiraBlockHelper({
  onAdd,
  instanceTags,
}: {
  onAdd: (pairs: [string, string][], prefix?: string) => void;
  instanceTags?: string[];
}) {
  const [block, setBlock] = useState<TesiraBlock>("level");
  const [tag, setTag] = useState("");
  const [channel, setChannel] = useState("1");
  const [step, setStep] = useState("3");
  const [line, setLine] = useState("1");
  const [error, setError] = useState<string | null>(null);
  const spec = TESIRA_BLOCKS.find((b) => b.value === block)!;

  const add = () => {
    const t = tag.trim();
    if (!t) return setError("Enter the block's instance tag from Tesira software.");
    if (/\s/.test(t)) {
      return setError("Instance tags with spaces aren't supported here — add those commands by hand.");
    }
    if (spec.channel && !/^\d+$/.test(channel)) {
      return setError("Channel must be a number: 0 for a ganged block, otherwise 1 or higher.");
    }
    if (block === "level" && (!/^\d+(\.\d+)?$/.test(step) || Number(step) <= 0)) {
      return setError("Step must be a positive number of dB.");
    }
    if (block === "voip" && (!/^\d+$/.test(line) || Number(line) < 1)) {
      return setError("Line must be 1 or higher.");
    }
    setError(null);
    onAdd(tesiraBlockCommands(block, t, channel, step, line), t);
    setTag("");
  };

  const field = (id: string, label: string, value: string, set: (v: string) => void, mode: "numeric" | "decimal") => (
    <div className="space-y-1">
      <label htmlFor={id} className={labelClass}>{label}</label>
      <input
        id={id}
        className={inputClass}
        inputMode={mode}
        value={value}
        onChange={(e) => { set(e.target.value.trim()); setError(null); }}
      />
    </div>
  );

  return (
    <div className="rounded-md border border-dashed p-3 space-y-2">
      <div className="flex items-center gap-2 text-xs font-medium text-foreground">
        <Wand2 className="h-3.5 w-3.5 text-primary" />
        Add commands for a DSP block
      </div>
      <div className="grid grid-cols-2 sm:grid-cols-[9rem_minmax(0,1fr)_5rem_5rem_auto] gap-2 items-end">
        <div className="col-span-2 sm:col-span-1 space-y-1">
          <label htmlFor="tesira-block" className={labelClass}>Block</label>
          <select
            id="tesira-block"
            className={inputClass}
            value={block}
            onChange={(e) => { setBlock(e.target.value as TesiraBlock); setError(null); }}
          >
            {TESIRA_BLOCKS.map((b) => (
              <option key={b.value} value={b.value}>{b.label}</option>
            ))}
          </select>
        </div>
        <div className="col-span-2 sm:col-span-1 space-y-1">
          <label htmlFor="tesira-tag" className={labelClass}>Instance tag</label>
          <input
            id="tesira-tag"
            list="tesira-command-tags"
            className={`${inputClass} font-mono text-xs`}
            value={tag}
            onChange={(e) => { setTag(e.target.value); setError(null); }}
            onKeyDown={(e) => {
              if (e.key === "Enter") { e.preventDefault(); add(); }
            }}
            placeholder={block === "voip" ? "VoIPControlStatus1" : block === "source" ? "SourceSelector1" : "master_level"}
            spellCheck={false}
            autoComplete="off"
          />
        </div>
        {spec.channel ? field("tesira-channel", "Channel", channel, setChannel, "numeric") : block === "voip" ? field("tesira-line", "Line", line, setLine, "numeric") : <div className="hidden sm:block" />}
        {block === "level" ? field("tesira-step", "Step (dB)", step, setStep, "decimal") : <div className="hidden sm:block" />}
        <Button type="button" size="sm" className="col-span-2 sm:col-span-1 h-9" onClick={add}>
          Add
        </Button>
      </div>
      <p className="text-[11px] text-muted-foreground">
        {spec.hint} If a name is already used, the new one is prefixed with the tag (e.g.{" "}
        <code>zone2_mute</code>).{spec.channel && " Use channel 0 for a ganged block."}
      </p>
      {instanceTags && <InstanceTagList id="tesira-command-tags" tags={instanceTags} />}
      {error && (
        <p className="text-xs [color:hsl(var(--destructive))]">{error}</p>
      )}
    </div>
  );
}

// InstanceTagList offers the DSP's instance tags to inputs with
// list={id}, and says where they come from.
export function InstanceTagList({ id, tags }: { id: string; tags: string[] }) {
  return (
    <>
      <datalist id={id}>
        {tags.map((t) => (
          <option key={t} value={t} />
        ))}
      </datalist>
      <p className="text-[11px] text-muted-foreground">
        {tags.length > 0
          ? `The instance tag box suggests the ${tags.length} block${tags.length === 1 ? "" : "s"} in the DSP's design, as its Collector last read them.`
          : "Once the device is saved and its Collector has connected, the instance tag box suggests the blocks in the DSP's design."}
      </p>
    </>
  );
}
