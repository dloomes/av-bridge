// Helpers for per-device command templates — the name → command-string map
// on a device (Tesira TTP strings, Telnet/REST payloads). A template may
// carry {placeholders}; the portal prompts for them when the command is
// sent and the bridge substitutes them (see fillTTPTemplate in the Tesira
// adapter, which uses the same identifier shape).

const PLACEHOLDER = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;

// Unique placeholder names in first-seen order, e.g.
// "{block} set level {channel} {level}" -> ["block", "channel", "level"].
export function templatePlaceholders(template: string | undefined): string[] {
  if (!template) return [];
  return Array.from(new Set(Array.from(template.matchAll(PLACEHOLDER), (m) => m[1])));
}

// Command names become button labels ("vol_up" -> "Vol Up"), routine-step
// targets and API identifiers, so keep them to a safe identifier shape.
export const COMMAND_NAME = /^[A-Za-z0-9_-]+$/;

// Normalise what an operator types into a command name: spaces become
// underscores and anything outside the identifier shape is dropped.
export function normaliseCommandName(raw: string): string {
  return raw.replace(/\s+/g, "_").replace(/[^A-Za-z0-9_-]/g, "");
}
