"use client";

import { useMemo, useState } from "react";
import { Download, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { useToast } from "@/components/toast";
import type { DeviceSummary } from "@/lib/types";
import { downloadDiagramPNG, downloadDiagramSVG, routineDiagramSVG } from "@/lib/routine-diagram";
import type { Step } from "./step-types";

// RoutineDiagram — the routine as a flow diagram (lib/routine-diagram),
// with PNG and SVG downloads. Read-only: it draws whatever the builder
// holds, saved or not.
export function RoutineDiagram({
  steps,
  title,
  devices,
}: {
  steps: Step[];
  title: string;
  devices: DeviceSummary[];
}) {
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const svg = useMemo(() => {
    const names = new Map(devices.map((d) => [d.id, d.name]));
    return routineDiagramSVG(steps, { title, deviceName: (id) => names.get(id) });
  }, [steps, title, devices]);

  const png = async () => {
    setBusy(true);
    try {
      await downloadDiagramPNG(svg, title);
    } catch (e) {
      toast({ title: "Couldn't download the diagram", description: (e as Error).message, variant: "destructive" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardContent className="p-4 space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <p className="mr-auto text-xs text-muted-foreground">
            How a run goes, step by step, and what happens when a step fails.
          </p>
          <Button variant="outline" size="sm" onClick={png} disabled={busy}>
            {busy ? (
              <Loader2 aria-hidden="true" className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Download aria-hidden="true" className="h-3.5 w-3.5" />
            )}
            PNG
          </Button>
          <Button variant="outline" size="sm" onClick={() => downloadDiagramSVG(svg, title)}>
            <Download aria-hidden="true" className="h-3.5 w-3.5" />
            SVG
          </Button>
        </div>
        {/* The diagram is drawn on white in both themes: it's the same
            image the downloads produce. */}
        <div
          role="img"
          aria-label={`Flow diagram of ${title}`}
          className="overflow-x-auto rounded-md border bg-white [&>svg]:mx-auto [&>svg]:block"
          dangerouslySetInnerHTML={{ __html: svg }}
        />
      </CardContent>
    </Card>
  );
}
