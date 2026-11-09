"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { Loader2 } from "lucide-react";
import { RoadmapView } from "@/components/roadmap-view";
import { api } from "@/lib/api";
import type { SharedRoadmap } from "@/lib/types";

type State =
  | { kind: "loading" }
  | { kind: "ok"; data: SharedRoadmap }
  | { kind: "invalid"; message: string }
  | { kind: "error"; message: string };

export function SharedRoadmapClient() {
  const params = useParams<{ token: string }>();
  const token = params?.token ?? "";
  const [state, setState] = useState<State>({ kind: "loading" });

  useEffect(() => {
    if (!token) return;
    const ctl = new AbortController();
    api
      .getSharedRoadmap(token, ctl.signal)
      .then((data) => setState({ kind: "ok", data }))
      .catch((e: Error & { status?: number }) => {
        if (ctl.signal.aborted) return;
        if (e.status === 404) {
          setState({ kind: "invalid", message: e.message || "This link isn't valid any more." });
        } else if (e.status === 429) {
          setState({ kind: "error", message: "Too many requests. Please try again in a minute." });
        } else {
          setState({ kind: "error", message: "The roadmap couldn't be loaded. Please try again later." });
        }
      });
    return () => ctl.abort();
  }, [token]);

  if (state.kind === "loading") {
    return (
      <div className="flex h-dvh items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }
  if (state.kind === "ok") {
    return <RoadmapView roadmap={state.data} sharedWith={state.data.shared_with} />;
  }
  return (
    <div className="flex h-dvh items-center justify-center p-6">
      <div className="max-w-md space-y-2 text-center" role="alert">
        <p className="text-base font-medium">{state.message}</p>
        {state.kind === "invalid" && (
          <p className="text-sm text-muted-foreground">Contact your Involve account manager for a new link.</p>
        )}
      </div>
    </div>
  );
}
