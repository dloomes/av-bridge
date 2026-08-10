"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { ArrowLeft, FileCode2, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { useToast } from "@/components/toast";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { isAdmin } from "@/lib/session";
import type { NightlyRoutineDetail, UpdateNightlyRoutineBody } from "@/lib/api";
import {
  RoutineBuilder,
  hydrateSteps,
  serializeSteps,
  type UIStep,
} from "@/components/routine-builder/RoutineBuilder";

// Room Readiness — routine editor.
//
// Two modes:
//   Builder (default) — structured DnD builder from @/components/routine-builder.
//   Advanced JSON     — raw textarea, kept as a fallback for unusual shapes
//                       the structured builder doesn't cover.
//
// Both edit the same underlying `steps` array — switching tabs re-syncs
// from whichever side was last edited.

type Mode = "builder" | "json";

export default function RoutineEditorPage() {
  const params = useParams<{ id: string }>();
  const routineID = params.id;
  const session = useSession();
  const canManage = isAdmin(session.user?.role) || !!session.user?.is_vendor;
  const router = useRouter();
  const { toast } = useToast();

  const [loaded, setLoaded] = useState<NightlyRoutineDetail | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [uiSteps, setUISteps] = useState<UIStep[]>([]);
  const [stepsText, setStepsText] = useState("");
  const [stepsError, setStepsError] = useState<string | null>(null);
  const [mode, setMode] = useState<Mode>("builder");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    const ctrl = new AbortController();
    api
      .getNightlyRoutine(routineID, ctrl.signal)
      .then((r) => {
        if (ctrl.signal.aborted) return;
        setLoaded(r);
        setName(r.name);
        setDescription(r.description ?? "");
        setUISteps(hydrateSteps(r.steps));
        setStepsText(JSON.stringify(r.steps, null, 2));
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) setLoadError((e as Error).message);
      });
    return () => ctrl.abort();
  }, [routineID]);

  // Builder is the source of truth by default. When the user switches
  // to Advanced JSON we serialize the current builder state into the
  // textarea; on switch back we parse the textarea back into UISteps.
  // This avoids drift while keeping either editor honest.
  const switchMode = (next: Mode) => {
    if (next === mode) return;
    if (mode === "builder" && next === "json") {
      setStepsText(JSON.stringify(serializeSteps(uiSteps), null, 2));
      setStepsError(null);
    } else if (mode === "json" && next === "builder") {
      // Try to parse the current JSON; if it fails, stay in JSON mode
      // and surface the error rather than silently discarding edits.
      const trimmed = stepsText.trim();
      if (trimmed === "") {
        setUISteps([]);
      } else {
        try {
          const parsed = JSON.parse(trimmed);
          if (!Array.isArray(parsed)) {
            setStepsError("Top-level value must be a JSON array.");
            return;
          }
          setUISteps(hydrateSteps(parsed as unknown[]));
          setStepsError(null);
        } catch (e) {
          setStepsError((e as Error).message);
          return;
        }
      }
    }
    setMode(next);
  };

  // Live JSON validation while in JSON mode — save button disables until
  // the textarea holds valid JSON.
  const parsedJSON = useMemo<
    { ok: true; value: unknown[] } | { ok: false; error: string }
  >(() => {
    if (mode !== "json") return { ok: true, value: [] };
    const trimmed = stepsText.trim();
    if (trimmed === "") return { ok: true, value: [] };
    try {
      const parsed = JSON.parse(trimmed);
      if (!Array.isArray(parsed)) {
        return { ok: false, error: "Top-level value must be a JSON array." };
      }
      return { ok: true, value: parsed as unknown[] };
    } catch (e) {
      return { ok: false, error: (e as Error).message };
    }
  }, [mode, stepsText]);

  useEffect(() => {
    if (mode === "json") setStepsError(parsedJSON.ok ? null : parsedJSON.error);
  }, [mode, parsedJSON]);

  // Effective steps to save = whichever editor is active.
  const effectiveSteps = useMemo<unknown[]>(() => {
    if (mode === "json") return parsedJSON.ok ? parsedJSON.value : loaded?.steps ?? [];
    return serializeSteps(uiSteps);
  }, [mode, parsedJSON, uiSteps, loaded]);

  const dirty =
    loaded !== null &&
    (name !== loaded.name ||
      description !== (loaded.description ?? "") ||
      JSON.stringify(effectiveSteps) !== JSON.stringify(loaded.steps));

  const handleSave = async () => {
    if (!loaded) return;
    if (mode === "json" && !parsedJSON.ok) return;
    const body: UpdateNightlyRoutineBody = {};
    if (name !== loaded.name) body.name = name;
    if (description !== (loaded.description ?? "")) body.description = description;
    if (JSON.stringify(effectiveSteps) !== JSON.stringify(loaded.steps)) {
      body.steps = effectiveSteps;
    }
    if (Object.keys(body).length === 0) return;
    setSaving(true);
    try {
      await api.updateNightlyRoutine(routineID, body);
      const fresh = await api.getNightlyRoutine(routineID);
      setLoaded(fresh);
      setName(fresh.name);
      setDescription(fresh.description ?? "");
      setUISteps(hydrateSteps(fresh.steps));
      setStepsText(JSON.stringify(fresh.steps, null, 2));
      toast({ title: "Routine saved", variant: "success" });
    } catch (e) {
      toast({
        title: "Save failed",
        description: (e as Error).message,
        variant: "destructive",
      });
    } finally {
      setSaving(false);
    }
  };

  const inputsDisabled = !canManage || saving;

  return (
    <div className="flex flex-col h-screen">
      <header className="border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-2.5">
          <div className="h-9 w-9 rounded-md bg-primary/10 flex items-center justify-center">
            <FileCode2 aria-hidden="true" className="h-4 w-4 text-primary" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <Link
                href="/nightly/routines"
                className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
              >
                <ArrowLeft aria-hidden="true" className="h-3 w-3" />
                All routines
              </Link>
            </div>
            <h1 className="text-xl font-semibold leading-tight">
              {loaded?.name ?? "Routine"}
            </h1>
          </div>
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto px-6 py-6">
        <div className="max-w-6xl space-y-6">
          {loadError && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
              {loadError}
            </div>
          )}

          {loaded === null && !loadError && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading routine…
            </div>
          )}

          {loaded !== null && (
            <>
              <Card>
                <CardContent className="p-6 space-y-4">
                  <div className="space-y-1">
                    <label htmlFor="name" className="text-xs font-medium">
                      Name
                    </label>
                    <input
                      id="name"
                      type="text"
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      disabled={inputsDisabled}
                      className="w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
                    />
                  </div>
                  <div className="space-y-1">
                    <label htmlFor="description" className="text-xs font-medium">
                      Description
                    </label>
                    <input
                      id="description"
                      type="text"
                      value={description}
                      onChange={(e) => setDescription(e.target.value)}
                      disabled={inputsDisabled}
                      placeholder="What this routine verifies (one line)"
                      className="w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50"
                    />
                  </div>
                </CardContent>
              </Card>

              <div className="flex items-center justify-between">
                <h2 className="text-sm font-semibold">Steps</h2>
                <div className="inline-flex rounded-md border bg-background p-0.5 text-xs">
                  <button
                    type="button"
                    onClick={() => switchMode("builder")}
                    className={`px-3 py-1 rounded-sm transition-colors ${
                      mode === "builder"
                        ? "bg-primary text-primary-foreground"
                        : "text-muted-foreground hover:text-foreground"
                    }`}
                  >
                    Builder
                  </button>
                  <button
                    type="button"
                    onClick={() => switchMode("json")}
                    className={`px-3 py-1 rounded-sm transition-colors ${
                      mode === "json"
                        ? "bg-primary text-primary-foreground"
                        : "text-muted-foreground hover:text-foreground"
                    }`}
                  >
                    Advanced JSON
                  </button>
                </div>
              </div>

              {mode === "builder" ? (
                <RoutineBuilder
                  steps={uiSteps}
                  onStepsChange={setUISteps}
                  disabled={inputsDisabled}
                />
              ) : (
                <Card>
                  <CardContent className="p-6 space-y-3">
                    <p className="text-xs text-muted-foreground">
                      JSON array of step objects. Step types recognised by the runner
                      (see docs/nightly-lifecycle-spec.md §7):{" "}
                      <span className="font-mono">power_on</span>,{" "}
                      <span className="font-mono">power_off</span>,{" "}
                      <span className="font-mono">wait</span>,{" "}
                      <span className="font-mono">device_command</span>,{" "}
                      <span className="font-mono">check_metric</span>,{" "}
                      <span className="font-mono">expect_status</span>.
                    </p>
                    <textarea
                      value={stepsText}
                      onChange={(e) => setStepsText(e.target.value)}
                      disabled={inputsDisabled}
                      spellCheck={false}
                      rows={22}
                      className={`w-full rounded-md border bg-background px-3 py-2 text-xs font-mono leading-relaxed disabled:opacity-50 ${
                        stepsError ? "border-[color:hsl(var(--destructive))]" : ""
                      }`}
                    />
                    {stepsError && (
                      <div className="text-xs [color:hsl(var(--destructive))]">
                        {stepsError}
                      </div>
                    )}
                  </CardContent>
                </Card>
              )}

              {canManage && (
                <div className="sticky bottom-0 -mx-6 border-t bg-background/95 backdrop-blur px-6 py-3">
                  <div className="max-w-6xl flex items-center justify-end gap-2">
                    <div className="mr-auto text-xs text-muted-foreground">
                      {dirty
                        ? "You have unsaved changes."
                        : loaded.updated_at
                          ? `Saved ${new Date(loaded.updated_at).toLocaleString()}`
                          : ""}
                    </div>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => router.push("/nightly/routines")}
                      disabled={saving}
                    >
                      Done
                    </Button>
                    <Button
                      size="sm"
                      onClick={handleSave}
                      disabled={
                        !dirty || saving || (mode === "json" && !parsedJSON.ok)
                      }
                    >
                      {saving && (
                        <Loader2
                          aria-hidden="true"
                          className="h-3.5 w-3.5 animate-spin"
                        />
                      )}
                      Save changes
                    </Button>
                  </div>
                </div>
              )}

              {!canManage && (
                <div className="text-xs text-muted-foreground italic">
                  Read-only — you need Manage Room Readiness to edit routines.
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
