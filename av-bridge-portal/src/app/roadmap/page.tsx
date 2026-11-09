"use client";

import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { RoadmapView } from "@/components/roadmap-view";
import { api } from "@/lib/api";
import type { Roadmap } from "@/lib/types";

// The product roadmap, for any signed-in user. Read-only here.
export default function RoadmapPage() {
  const [roadmap, setRoadmap] = useState<Roadmap | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ctl = new AbortController();
    api
      .getRoadmap(ctl.signal)
      .then((r) => setRoadmap(r))
      .catch((e) => {
        if (!ctl.signal.aborted) setError((e as Error).message);
      });
    return () => ctl.abort();
  }, []);

  if (error) {
    return (
      <div className="p-6 text-sm text-destructive" role="alert">
        Couldn&apos;t load the roadmap: {error}
      </div>
    );
  }
  if (!roadmap) {
    return (
      <div className="flex h-app items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }
  return <RoadmapView roadmap={roadmap} />;
}
