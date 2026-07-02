"use client";

import { useMemo, useState } from "react";
import { AlertCircle, CheckCircle2, Loader2, Send } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { BulkCommandResult, DeviceSummary } from "@/lib/types";

interface Props {
  devices: DeviceSummary[];
  onClose: () => void;
}

// BulkCommandForm — pick a subset of devices, name a command, optionally
// provide args JSON, fire the bulk endpoint. Per-device results come back
// inline; portal doesn't wait for terminal (bulk fires don't wait — see the
// backend), so this page shows "queued" per device with the command_id
// linked out for follow-up.
//
// Command name is a free-text field because commands vary per device type
// (mute on Tesira, power_off on Bravia, etc). The backend validates against
// each device's command map — devices that don't recognise the command
// come back with a per-device error in the response.
export function BulkCommandForm({ devices, onClose }: Props) {
  const [filter, setFilter] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [command, setCommand] = useState("");
  const [argsText, setArgsText] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [results, setResults] = useState<BulkCommandResult[] | null>(null);

  const filteredDevices = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return devices;
    return devices.filter(
      (d) =>
        d.name.toLowerCase().includes(q) ||
        d.location.toLowerCase().includes(q) ||
        d.type.toLowerCase().includes(q)
    );
  }, [devices, filter]);

  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const toggleAll = () =>
    setSelected((prev) => {
      if (filteredDevices.every((d) => prev.has(d.id))) {
        const next = new Set(prev);
        for (const d of filteredDevices) next.delete(d.id);
        return next;
      }
      const next = new Set(prev);
      for (const d of filteredDevices) next.add(d.id);
      return next;
    });

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    if (selected.size === 0) {
      setError("Select at least one device");
      return;
    }
    if (!command.trim()) {
      setError("Command name is required");
      return;
    }
    let args: Record<string, unknown> | undefined;
    if (argsText.trim()) {
      try {
        const parsed = JSON.parse(argsText);
        if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
          throw new Error("args must be a JSON object");
        }
        args = parsed as Record<string, unknown>;
      } catch (e) {
        setError(`args: ${(e as Error).message}`);
        return;
      }
    }

    setSubmitting(true);
    try {
      const resp = await api.sendBulkCommand({
        device_ids: Array.from(selected),
        name: command.trim(),
        args,
      });
      setResults(resp.results);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  // Results view — replaces the picker once a send returns so the user sees
  // per-device outcomes without losing context. "Send another" resets.
  if (results) {
    const okCount = results.filter((r) => r.command_id).length;
    const errCount = results.length - okCount;
    return (
      <div className="space-y-3">
        <div className="rounded-md border p-3 text-sm space-y-1">
          <div>
            <span className="font-semibold text-emerald-600">{okCount}</span>{" "}
            queued
            {errCount > 0 && (
              <>
                {" · "}
                <span className="font-semibold text-red-600">{errCount}</span>{" "}
                errored
              </>
            )}
          </div>
          <div className="text-[11px] text-muted-foreground">
            Bulk sends don't wait for device replies. Track individual command
            outcomes on the device pages or via the activity feed.
          </div>
        </div>

        <ul className="max-h-72 overflow-y-auto space-y-1">
          {results.map((r) => {
            const dev = devices.find((d) => d.id === r.device_id);
            return (
              <li
                key={r.device_id}
                className="flex items-center gap-2 text-sm border-b py-1 last:border-b-0"
              >
                {r.command_id ? (
                  <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500 shrink-0" />
                ) : (
                  <AlertCircle className="h-3.5 w-3.5 text-red-500 shrink-0" />
                )}
                <span className="flex-1 truncate">{dev?.name ?? r.device_id}</span>
                {r.command_id && (
                  <span className="font-mono text-[10px] text-muted-foreground">
                    {r.command_id.slice(0, 8)}
                  </span>
                )}
                {r.error && (
                  <span className="text-[11px] text-red-600 truncate">{r.error}</span>
                )}
              </li>
            );
          })}
        </ul>

        <div className="flex justify-end gap-2 pt-2 border-t">
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              setResults(null);
              setSelected(new Set());
              setCommand("");
              setArgsText("");
            }}
          >
            Send another
          </Button>
          <Button type="button" onClick={onClose}>
            Done
          </Button>
        </div>
      </div>
    );
  }

  const allFilteredSelected =
    filteredDevices.length > 0 && filteredDevices.every((d) => selected.has(d.id));

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      <div>
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Command name
        </label>
        <input
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm font-mono"
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          placeholder="e.g. power_off, mute, set_input"
          required
        />
        <p className="mt-1 text-[11px] text-muted-foreground">
          Command names vary per device — devices that don't have this command
          in their config will return an error per device.
        </p>
      </div>

      <details className="rounded-md border bg-muted/30 p-2">
        <summary className="cursor-pointer text-xs font-medium text-muted-foreground uppercase tracking-wide">
          Args (JSON, optional)
        </summary>
        <textarea
          rows={3}
          className="mt-2 w-full rounded-md border border-input bg-background px-3 py-2 text-xs font-mono"
          value={argsText}
          onChange={(e) => setArgsText(e.target.value)}
          placeholder='{"level": -20}'
        />
      </details>

      <div>
        <div className="flex items-center justify-between mb-1">
          <label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
            Devices ({selected.size} selected)
          </label>
          <button
            type="button"
            onClick={toggleAll}
            className="text-[11px] text-muted-foreground hover:text-foreground"
          >
            {allFilteredSelected ? "Deselect visible" : "Select visible"}
          </button>
        </div>
        <input
          className="h-8 w-full rounded-md border border-input bg-background px-3 text-sm mb-2"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter by name, type, or location…"
        />
        <ul className="max-h-64 overflow-y-auto rounded-md border">
          {filteredDevices.length === 0 && (
            <li className="p-3 text-center text-xs text-muted-foreground">
              No devices match.
            </li>
          )}
          {filteredDevices.map((d) => (
            <li key={d.id} className="border-b last:border-b-0">
              <label className="flex items-center gap-2 px-3 py-1.5 text-sm hover:bg-accent/40 cursor-pointer">
                <input
                  type="checkbox"
                  checked={selected.has(d.id)}
                  onChange={() => toggle(d.id)}
                  className="h-4 w-4"
                />
                <span className="flex-1 truncate">{d.name}</span>
                <span className="text-[11px] text-muted-foreground truncate">
                  {d.type} · {d.location || "—"}
                </span>
              </label>
            </li>
          ))}
        </ul>
      </div>

      <div className="flex justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onClose} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting}>
          {submitting ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Send className="h-3.5 w-3.5" />
          )}
          Send to {selected.size} device{selected.size === 1 ? "" : "s"}
        </Button>
      </div>
    </form>
  );
}
