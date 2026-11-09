"use client";

import { useEffect, useState } from "react";
import { ExternalLink, Loader2, Signpost } from "lucide-react";
import { Button } from "@/components/ui/button";
import { BackLink } from "@/components/back-link";
import { ItemsSection } from "@/components/roadmap-editor/items";
import { LinksSection } from "@/components/roadmap-editor/links";
import { SettingsSection } from "@/components/roadmap-editor/settings";
import { ShippedSection } from "@/components/roadmap-editor/shipped";
import { PeriodsSection, ThemesSection } from "@/components/roadmap-editor/structure";
import { ErrorBox } from "@/components/roadmap-editor/shared";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { plainError } from "@/lib/download";
import { getUser, updateUser } from "@/lib/session";
import type { Roadmap, RoadmapLink } from "@/lib/types";

// /helpdesk/roadmap: edit the product roadmap and manage its share links.
// Only Involve users flagged as roadmap editors may use it; the cloud checks
// the same flag on every write, so this page only decides what to show.
export default function RoadmapEditorPage() {
  const session = useSession();
  const flagged = !!session.user?.roadmap_editor;
  // The stored session learns the flag at sign-in. Someone made an editor
  // since then is re-checked once before being told no.
  const [rechecked, setRechecked] = useState(false);
  const [roadmap, setRoadmap] = useState<Roadmap | null>(null);
  const [links, setLinks] = useState<RoadmapLink[] | null>(null);
  const [roadmapError, setRoadmapError] = useState<string | null>(null);
  const [linksError, setLinksError] = useState<string | null>(null);

  useEffect(() => {
    if (!session.hydrated || !session.user || flagged) return;
    if (!session.user.is_vendor) {
      setRechecked(true);
      return;
    }
    let cancelled = false;
    api
      .whoami()
      .then((who) => {
        if (cancelled) return;
        const cur = getUser();
        if (cur && !!who.roadmap_editor !== !!cur.roadmap_editor) {
          updateUser({ ...cur, roadmap_editor: who.roadmap_editor });
        }
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setRechecked(true);
      });
    return () => {
      cancelled = true;
    };
  }, [session.hydrated, session.user, flagged]);

  useEffect(() => {
    if (!flagged) return;
    const ctl = new AbortController();
    api
      .getRoadmap(ctl.signal)
      .then(setRoadmap)
      .catch((e) => {
        if (!ctl.signal.aborted) setRoadmapError(plainError(e));
      });
    api
      .listRoadmapLinks(ctl.signal)
      .then((r) => setLinks(r.links))
      .catch((e) => {
        if (!ctl.signal.aborted) setLinksError(plainError(e));
      });
    return () => ctl.abort();
  }, [flagged]);

  if (!session.hydrated || (!flagged && !rechecked)) {
    return (
      <div className="flex h-app items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (!flagged) {
    return (
      <div className="min-h-app">
        <div className="mx-auto max-w-3xl space-y-3 p-6">
          <BackLink href="/helpdesk" label="Helpdesk" iconClassName="h-3.5 w-3.5" />
          <p className="text-sm" role="alert">
            Only roadmap editors can change the roadmap.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-app">
      <div className="mx-auto max-w-5xl space-y-4 p-6">
        <BackLink href="/helpdesk" label="Helpdesk" iconClassName="h-3.5 w-3.5" />
        <header className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-amber-500/10 ring-1 ring-amber-500/30">
              <Signpost className="h-4 w-4 text-amber-600" />
            </div>
            <div>
              <h1 className="text-xl font-semibold">Roadmap editor</h1>
              <p className="text-sm text-muted-foreground">
                Changes show on the roadmap page straight away.
              </p>
            </div>
          </div>
          <Button asChild size="sm" variant="outline">
            <a href="/roadmap" target="_blank" rel="noopener noreferrer">
              <ExternalLink className="h-3.5 w-3.5" />
              Preview
            </a>
          </Button>
        </header>

        <ErrorBox message={roadmapError && `Couldn't load the roadmap: ${roadmapError}`} />
        {!roadmap && !roadmapError && (
          <div className="flex justify-center p-10">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
          </div>
        )}
        {roadmap && (
          <>
            <SettingsSection roadmap={roadmap} onChange={setRoadmap} />
            <ItemsSection roadmap={roadmap} onChange={setRoadmap} />
            <PeriodsSection roadmap={roadmap} onChange={setRoadmap} />
            <ThemesSection roadmap={roadmap} onChange={setRoadmap} />
            <ShippedSection roadmap={roadmap} onChange={setRoadmap} />
          </>
        )}
        <LinksSection links={links} loadError={linksError} onChange={setLinks} />
      </div>
    </div>
  );
}
