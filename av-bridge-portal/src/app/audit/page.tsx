"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { ArrowLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ConnectionIndicator } from "@/components/connection-indicator";
import { AuditFeed } from "@/components/audit-feed";

// Customer-wide audit page. Filters are query-string driven so the device
// detail page can deep-link with ?target_kind=device&target_id=<uuid> — same
// component, narrower feed.
export default function AuditPage() {
  const params = useSearchParams();
  const targetKind = params.get("target_kind") ?? undefined;
  const targetId = params.get("target_id") ?? undefined;
  const filtered = !!(targetKind || targetId);

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b bg-card/50 px-6 py-4">
        <div className="flex items-center gap-3 min-w-0">
          <Button asChild variant="ghost" size="sm">
            <Link href="/">
              <ArrowLeft className="h-3.5 w-3.5" />
              Back
            </Link>
          </Button>
          <div className="min-w-0">
            <h1 className="font-semibold">Activity</h1>
            <p className="text-xs text-muted-foreground">
              {filtered
                ? `Filtered to ${targetKind ?? "any"}${targetId ? `:${targetId}` : ""}`
                : "All portal changes across this customer, most recent first."}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          {filtered && (
            <Button asChild variant="outline" size="sm">
              <Link href="/audit">Clear filter</Link>
            </Button>
          )}
          <ConnectionIndicator />
        </div>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto max-w-3xl space-y-4 p-6">
          <AuditFeed
            targetKind={targetKind}
            targetId={targetId}
            emptyHint={
              filtered
                ? "Nothing has happened to this target yet."
                : "No audit entries yet — everything's quiet."
            }
          />
        </div>
      </div>
    </div>
  );
}
