"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  ArrowLeft,
  FileCode2,
  Loader2,
  Pencil,
  Plus,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Modal } from "@/components/modal";
import { useToast } from "@/components/toast";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { isAdmin } from "@/lib/session";
import { CANONICAL_ROUTINE } from "./canonical";
import type { NightlyRoutineRow } from "@/lib/api";

// Room Readiness — routine list page.
//
// Slice 2B. Routines are the reusable functional-test definitions the
// Phase B runner will execute after power-on. This page provides list +
// delete + create-with-template flows. The per-routine editor lives at
// /nightly/routines/[id]/page.tsx.

export default function RoutinesListPage() {
  const session = useSession();
  const canManage = isAdmin(session.user?.role) || !!session.user?.is_vendor;
  const router = useRouter();
  const { toast } = useToast();

  const [routines, setRoutines] = useState<NightlyRoutineRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<NightlyRoutineRow | null>(null);
  const [deletingBusy, setDeletingBusy] = useState(false);

  const loadRoutines = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await api.listNightlyRoutines(signal);
      if (signal?.aborted) return;
      setRoutines(list);
      setLoadError(null);
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void loadRoutines(ctrl.signal);
    return () => ctrl.abort();
  }, [loadRoutines]);

  const handleCreate = async (fromTemplate: boolean) => {
    setCreating(true);
    try {
      const res = await api.createNightlyRoutine({
        name: fromTemplate ? CANONICAL_ROUTINE.name : "Untitled routine",
        description: fromTemplate ? CANONICAL_ROUTINE.description : "",
        steps: fromTemplate ? CANONICAL_ROUTINE.steps : [],
      });
      // Land the user straight in the editor so they can immediately
      // tweak the name / steps — that's the flow this page exists for.
      router.push(`/nightly/routines/${res.id}`);
    } catch (e) {
      toast({
        title: "Create failed",
        description: (e as Error).message,
        variant: "destructive",
      });
      setCreating(false);
    }
  };

  const handleDelete = async () => {
    if (!deleting) return;
    setDeletingBusy(true);
    try {
      await api.deleteNightlyRoutine(deleting.id);
      setDeleting(null);
      await loadRoutines();
      toast({
        title: `Deleted "${deleting.name}"`,
        description:
          "Schedules referencing this routine now run power-cycle only until you assign a new one.",
        variant: "success",
      });
    } catch (e) {
      toast({
        title: "Delete failed",
        description: (e as Error).message,
        variant: "destructive",
      });
    } finally {
      setDeletingBusy(false);
    }
  };

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
                href="/nightly/schedule"
                className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
              >
                <ArrowLeft aria-hidden="true" className="h-3 w-3" />
                Room Readiness
              </Link>
            </div>
            <h1 className="text-xl font-semibold leading-tight">
              Test routines
            </h1>
            <p className="text-sm text-muted-foreground leading-tight">
              Reusable step sequences the readiness runner executes after
              power-on.
            </p>
          </div>
          {canManage && (
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={creating}
                onClick={() => handleCreate(false)}
              >
                {creating ? (
                  <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin" />
                ) : (
                  <Plus aria-hidden="true" className="h-4 w-4" />
                )}
                Blank routine
              </Button>
              <Button
                size="sm"
                disabled={creating}
                onClick={() => handleCreate(true)}
              >
                {creating ? (
                  <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin" />
                ) : (
                  <Plus aria-hidden="true" className="h-4 w-4" />
                )}
                From standard template
              </Button>
            </div>
          )}
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto px-6 py-6">
        <div className="max-w-3xl space-y-4">
          {loadError && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
              {loadError}
            </div>
          )}

          {routines === null && !loadError && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading routines…
            </div>
          )}

          {routines !== null && routines.length === 0 && (
            <Card>
              <CardContent className="p-10 text-center space-y-3">
                <div
                  aria-hidden="true"
                  className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary/10"
                >
                  <FileCode2 className="h-6 w-6 [color:hsl(var(--primary))]" />
                </div>
                <div className="text-base font-semibold">No routines yet</div>
                <p className="mx-auto max-w-md text-sm text-muted-foreground">
                  A routine is a JSON step sequence — power on the room, place a
                  test call, check the microphones hear audio, hang up.
                  The scheduler runs the assigned routine after power-on.
                </p>
                {canManage && (
                  <div className="pt-1 flex justify-center gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={creating}
                      onClick={() => handleCreate(false)}
                    >
                      <Plus aria-hidden="true" className="h-4 w-4" />
                      Blank routine
                    </Button>
                    <Button
                      size="sm"
                      disabled={creating}
                      onClick={() => handleCreate(true)}
                    >
                      <Plus aria-hidden="true" className="h-4 w-4" />
                      From standard template
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          {routines !== null && routines.length > 0 && (
            <Card>
              <CardContent className="p-0">
                <div className="overflow-x-auto">
                  <table className="w-full min-w-[560px] text-sm">
                    <thead>
                      <tr className="border-b bg-muted/40 text-left text-[10px] uppercase tracking-wider text-muted-foreground">
                        <th scope="col" className="px-4 py-3 font-medium">
                          Routine
                        </th>
                        <th scope="col" className="px-4 py-3 font-medium">
                          Steps
                        </th>
                        <th scope="col" className="px-4 py-3 font-medium">
                          Last updated
                        </th>
                        <th scope="col" className="px-4 py-3 font-medium text-right">
                          <span className="sr-only">Actions</span>
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {routines.map((r) => (
                        <tr
                          key={r.id}
                          className="border-b last:border-0 transition-colors hover:bg-primary/[0.04]"
                        >
                          <td className="px-4 py-3">
                            <Link
                              href={`/nightly/routines/${r.id}`}
                              className="font-medium text-foreground hover:underline"
                            >
                              {r.name}
                            </Link>
                            {r.description && (
                              <div className="text-xs text-muted-foreground mt-0.5">
                                {r.description}
                              </div>
                            )}
                          </td>
                          <td className="px-4 py-3 text-xs text-muted-foreground">
                            {r.step_count} {r.step_count === 1 ? "step" : "steps"}
                          </td>
                          <td className="px-4 py-3 text-xs text-muted-foreground">
                            {new Date(r.updated_at).toLocaleString()}
                          </td>
                          <td className="px-4 py-3">
                            <div className="flex items-center justify-end gap-0.5">
                              <Link href={`/nightly/routines/${r.id}`}>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="h-8 w-8"
                                  aria-label={`Edit ${r.name}`}
                                >
                                  <Pencil
                                    aria-hidden="true"
                                    className="h-3.5 w-3.5"
                                  />
                                </Button>
                              </Link>
                              {canManage && (
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="h-8 w-8 hover:[color:hsl(var(--destructive))]"
                                  aria-label={`Delete ${r.name}`}
                                  onClick={() => setDeleting(r)}
                                >
                                  <Trash2
                                    aria-hidden="true"
                                    className="h-3.5 w-3.5"
                                  />
                                </Button>
                              )}
                            </div>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </CardContent>
            </Card>
          )}
        </div>
      </div>

      {deleting && (
        <Modal
          open
          onClose={() => {
            if (!deletingBusy) setDeleting(null);
          }}
          title={`Delete "${deleting.name}"?`}
          wide={false}
        >
          <p className="text-sm text-muted-foreground">
            Schedules referencing this routine will lose their test and run
            power-cycle only until you assign a new one. Runs already
            executed keep their step results.
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => setDeleting(null)}
              disabled={deletingBusy}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={handleDelete}
              disabled={deletingBusy}
            >
              {deletingBusy && (
                <Loader2 aria-hidden="true" className="h-3.5 w-3.5 animate-spin" />
              )}
              Delete
            </Button>
          </div>
        </Modal>
      )}
    </div>
  );
}
