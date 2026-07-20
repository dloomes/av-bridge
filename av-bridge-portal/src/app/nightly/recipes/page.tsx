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
import { CANONICAL_RECIPE } from "./canonical";
import type { NightlyRecipeRow } from "@/lib/api";

// Room Readiness — recipe list page.
//
// Slice 2B. Recipes are the reusable functional-test definitions the
// Phase B runner will execute after power-on. This page provides list +
// delete + create-with-template flows. The per-recipe editor lives at
// /nightly/recipes/[id]/page.tsx.

export default function RecipesListPage() {
  const session = useSession();
  const canManage = isAdmin(session.user?.role) || !!session.user?.is_vendor;
  const router = useRouter();
  const { toast } = useToast();

  const [recipes, setRecipes] = useState<NightlyRecipeRow[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<NightlyRecipeRow | null>(null);
  const [deletingBusy, setDeletingBusy] = useState(false);

  const loadRecipes = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await api.listNightlyRecipes(signal);
      if (signal?.aborted) return;
      setRecipes(list);
      setLoadError(null);
    } catch (e) {
      if (!signal?.aborted) setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    void loadRecipes(ctrl.signal);
    return () => ctrl.abort();
  }, [loadRecipes]);

  const handleCreate = async (fromTemplate: boolean) => {
    setCreating(true);
    try {
      const res = await api.createNightlyRecipe({
        name: fromTemplate ? CANONICAL_RECIPE.name : "Untitled recipe",
        description: fromTemplate ? CANONICAL_RECIPE.description : "",
        steps: fromTemplate ? CANONICAL_RECIPE.steps : [],
      });
      // Land the user straight in the editor so they can immediately
      // tweak the name / steps — that's the flow this page exists for.
      router.push(`/nightly/recipes/${res.id}`);
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
      await api.deleteNightlyRecipe(deleting.id);
      setDeleting(null);
      await loadRecipes();
      toast({
        title: `Deleted "${deleting.name}"`,
        description:
          "Schedules referencing this recipe now run power-cycle only until you assign a new one.",
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
              Test recipes
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
                Blank recipe
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

          {recipes === null && !loadError && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading recipes…
            </div>
          )}

          {recipes !== null && recipes.length === 0 && (
            <Card>
              <CardContent className="p-10 text-center space-y-3">
                <div
                  aria-hidden="true"
                  className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary/10"
                >
                  <FileCode2 className="h-6 w-6 [color:hsl(var(--primary))]" />
                </div>
                <div className="text-base font-semibold">No recipes yet</div>
                <p className="mx-auto max-w-md text-sm text-muted-foreground">
                  A recipe is a JSON step sequence — power on the room, place a
                  test call, check the microphones hear audio, hang up.
                  The scheduler runs the assigned recipe after power-on.
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
                      Blank recipe
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

          {recipes !== null && recipes.length > 0 && (
            <Card>
              <CardContent className="p-0">
                <div className="overflow-x-auto">
                  <table className="w-full min-w-[560px] text-sm">
                    <thead>
                      <tr className="border-b bg-muted/40 text-left text-[10px] uppercase tracking-wider text-muted-foreground">
                        <th scope="col" className="px-4 py-3 font-medium">
                          Recipe
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
                      {recipes.map((r) => (
                        <tr
                          key={r.id}
                          className="border-b last:border-0 transition-colors hover:bg-primary/[0.04]"
                        >
                          <td className="px-4 py-3">
                            <Link
                              href={`/nightly/recipes/${r.id}`}
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
                              <Link href={`/nightly/recipes/${r.id}`}>
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
            Schedules referencing this recipe will lose their test and run
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
