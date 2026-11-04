"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Download, FileText, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { BackLink } from "@/components/back-link";
import { useSession } from "@/hooks/useSession";
import { api, type DeletionItem } from "@/lib/api";
import { plainError, saveDownload } from "@/lib/download";
import { formatDay } from "@/lib/offboarding";

// Deletions: every customer offboarding, newest first, with the certificate
// for each. Involve staff only.
export default function DeletionsPage() {
  const session = useSession();
  const router = useRouter();
  const [items, setItems] = useState<DeletionItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const isVendor = !!session.user?.is_vendor;

  useEffect(() => {
    if (!session.hydrated) return;
    if (session.user && !session.user.is_vendor) router.replace("/");
  }, [session.hydrated, session.user, router]);

  useEffect(() => {
    if (!session.hydrated || !isVendor) return;
    const ctrl = new AbortController();
    api
      .listDeletions(ctrl.signal)
      .then(setItems)
      .catch((e) => {
        if (!ctrl.signal.aborted) setError(plainError(e));
      });
    return () => ctrl.abort();
  }, [session.hydrated, isVendor]);

  const csv = async (id: string) => {
    if (busyId) return;
    setBusyId(id);
    setError(null);
    try {
      saveDownload(await api.downloadDeletionCSV(id));
    } catch (e) {
      setError(plainError(e));
    } finally {
      setBusyId(null);
    }
  };

  if (!session.hydrated || !isVendor) return null;

  return (
    <div className="min-h-app">
      <div className="max-w-5xl mx-auto p-4 sm:p-6 space-y-4">
        <header className="flex items-center gap-3">
          <Button asChild variant="ghost" size="sm" className="shrink-0">
            <BackLink href="/helpdesk" label="Helpdesk" iconClassName="h-3.5 w-3.5" />
          </Button>
          <div className="h-9 w-9 shrink-0 rounded-md bg-primary/10 flex items-center justify-center">
            <FileText aria-hidden="true" className="h-4 w-4 text-primary" />
          </div>
          <div className="min-w-0">
            <h1 className="text-2xl font-semibold leading-tight">Deletions</h1>
            <p className="text-sm text-muted-foreground leading-tight">
              Customers that have been offboarded, and their deletion certificates.
            </p>
          </div>
        </header>

        {error && (
          <div
            role="alert"
            className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]"
          >
            {error}
          </div>
        )}

        {items === null && !error ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : items && items.length === 0 ? (
          <Card>
            <CardContent className="p-8 text-center text-sm text-muted-foreground">
              No customers have been offboarded.
            </CardContent>
          </Card>
        ) : items ? (
          <Card>
            <CardContent className="p-0 overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-xs text-muted-foreground">
                    <th className="px-3 py-2 font-medium">Customer</th>
                    <th className="px-3 py-2 font-medium">Started</th>
                    <th className="px-3 py-2 font-medium">By</th>
                    <th className="px-3 py-2 font-medium">Deleted or cancelled</th>
                    <th className="px-3 py-2 font-medium">Downloads</th>
                    <th className="px-3 py-2 font-medium">
                      <span className="sr-only">Certificate</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y">
                  {items.map((d) => (
                    <tr key={d.id}>
                      <td className="px-3 py-2 font-medium break-words">{d.customer_name}</td>
                      <td className="px-3 py-2 whitespace-nowrap">{formatDay(d.started_at)}</td>
                      <td className="px-3 py-2 break-all">{d.started_by}</td>
                      <td className="px-3 py-2 whitespace-nowrap">
                        {d.deleted_at
                          ? `Deleted ${formatDay(d.deleted_at)}`
                          : d.cancelled_at
                          ? `Cancelled ${formatDay(d.cancelled_at)}`
                          : d.delete_after
                          ? `Due ${formatDay(d.delete_after)}`
                          : "Waiting"}
                      </td>
                      <td className="px-3 py-2 tabular-nums">{d.downloads.length}</td>
                      <td className="px-3 py-2 text-right">
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => csv(d.id)}
                          disabled={busyId !== null}
                          aria-label={`Download certificate CSV for ${d.customer_name}`}
                        >
                          {busyId === d.id ? (
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                          ) : (
                            <Download className="h-3.5 w-3.5" />
                          )}
                          CSV
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </CardContent>
          </Card>
        ) : null}
      </div>
    </div>
  );
}
