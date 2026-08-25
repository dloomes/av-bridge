"use client";

// BuildingsMap renders a Mapbox map with a coloured pin per building. It
// lives in its own file so /map and other consumers can dynamic-import
// it with { ssr: false } — Mapbox GL touches `window` and DOM APIs on
// module load, which trips Next.js's server render pass otherwise.
//
// Data contract: caller passes buildings (already coord-filtered) and a
// map of building_name → worst device status. Anything without both
// lat/lon is expected to be filtered upstream; we still guard here.

import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import Map, {
  Marker,
  NavigationControl,
  Popup,
  type MapRef,
} from "react-map-gl";
import type { LngLatBoundsLike } from "mapbox-gl";
import "mapbox-gl/dist/mapbox-gl.css";
import { AlertCircle } from "lucide-react";
import { cn } from "@/lib/utils";
import type { BuildingRow, DeviceStatus } from "@/lib/types";

// Worst-first — the same precedence the overview tiles use. Consuming
// pages compute per-building worst status via the shared helper so the
// map and the tile grid never disagree.
type WorstStatus = DeviceStatus;

const STATUS_TINT: Record<WorstStatus, { pin: string; ring: string; label: string }> = {
  offline: {
    pin: "bg-destructive text-destructive-foreground",
    ring: "ring-destructive/40",
    label: "Offline",
  },
  degraded: {
    pin: "bg-warning text-warning-foreground",
    ring: "ring-warning/40",
    label: "Degraded",
  },
  unknown: {
    pin: "bg-muted text-muted-foreground",
    ring: "ring-border",
    label: "Unknown",
  },
  online: {
    pin: "bg-success text-success-foreground",
    ring: "ring-success/40",
    label: "Online",
  },
};

export interface BuildingsMapEntry {
  building: BuildingRow;
  worst: WorstStatus;
  totals: {
    total: number;
    online: number;
    offline: number;
    degraded: number;
    unknown: number;
  };
}

interface BuildingsMapProps {
  entries: BuildingsMapEntry[];
  mapboxToken: string;
  className?: string;
}

export function BuildingsMap({ entries, mapboxToken, className }: BuildingsMapProps) {
  const mapRef = useRef<MapRef | null>(null);
  const [openId, setOpenId] = useState<string | null>(null);
  // The map is only safe to command (fitBounds, flyTo) after its `load`
  // event fires — before then the underlying mapbox-gl instance rejects
  // camera calls silently. We track load state so the fit-to-bounds
  // effect can wait for the map to be ready on first mount.
  const [mapLoaded, setMapLoaded] = useState(false);

  // Filter down to plottable entries once — a caller might pass rows
  // whose coords haven't been set yet.
  const plottable = useMemo(
    () =>
      entries.filter(
        (e): e is BuildingsMapEntry =>
          typeof e.building.latitude === "number" &&
          typeof e.building.longitude === "number"
      ),
    [entries]
  );

  const bounds = useMemo<LngLatBoundsLike | null>(() => {
    if (plottable.length === 0) return null;
    let minLon = Infinity,
      maxLon = -Infinity,
      minLat = Infinity,
      maxLat = -Infinity;
    for (const e of plottable) {
      const lon = e.building.longitude!;
      const lat = e.building.latitude!;
      if (lon < minLon) minLon = lon;
      if (lon > maxLon) maxLon = lon;
      if (lat < minLat) minLat = lat;
      if (lat > maxLat) maxLat = lat;
    }
    return [
      [minLon, minLat],
      [maxLon, maxLat],
    ];
  }, [plottable]);

  // Whenever the bounds materially change, re-fit. Zooming out to the
  // whole world when the first building lands is jarring, so a single
  // pin flies to a sensible zoom instead. Guarded on mapLoaded so the
  // very first fit (which happens on initial mount) isn't dropped.
  useEffect(() => {
    if (!mapRef.current || !mapLoaded) return;
    if (plottable.length === 1) {
      const only = plottable[0];
      mapRef.current.flyTo({
        center: [only.building.longitude!, only.building.latitude!],
        zoom: 13,
        essential: true,
      });
      return;
    }
    if (bounds) {
      mapRef.current.fitBounds(bounds, {
        padding: 80,
        duration: 600,
        maxZoom: 14,
      });
    }
  }, [bounds, plottable, mapLoaded]);

  if (!mapboxToken) {
    return (
      <div
        className={cn(
          "flex h-[420px] items-center justify-center rounded-lg border border-dashed border-border bg-muted/30 px-6 text-center",
          className
        )}
      >
        <div className="max-w-md space-y-2">
          <AlertCircle className="mx-auto h-8 w-8 text-muted-foreground" />
          <div className="text-sm font-medium">Map isn&apos;t configured yet</div>
          <p className="text-xs text-muted-foreground">
            Ops needs to set <code>NEXT_PUBLIC_MAPBOX_TOKEN</code> on the
            portal deploy before the map view can render.
          </p>
        </div>
      </div>
    );
  }

  if (plottable.length === 0) {
    return (
      <div
        className={cn(
          "flex h-[420px] items-center justify-center rounded-lg border border-dashed border-border bg-muted/30 px-6 text-center",
          className
        )}
      >
        <div className="max-w-md space-y-2">
          <AlertCircle className="mx-auto h-8 w-8 text-muted-foreground" />
          <div className="text-sm font-medium">No buildings on the map yet</div>
          <p className="text-xs text-muted-foreground">
            Add latitude and longitude to a building on the Locations page
            and it will appear here.
          </p>
        </div>
      </div>
    );
  }

  const openEntry = openId ? plottable.find((e) => e.building.id === openId) : null;

  return (
    <div className={cn("relative overflow-hidden rounded-lg border border-border", className)}>
      <Map
        ref={mapRef}
        mapboxAccessToken={mapboxToken}
        // Standard full-colour streets style — roads, land use, and
        // labels all in their natural palette. Swap for outdoors-v12
        // if terrain becomes useful; light-v11 was the muted default
        // we started with.
        mapStyle="mapbox://styles/mapbox/streets-v12"
        initialViewState={{
          longitude: plottable[0].building.longitude!,
          latitude: plottable[0].building.latitude!,
          zoom: 10,
        }}
        style={{ width: "100%", height: 480 }}
        attributionControl={true}
        onLoad={() => setMapLoaded(true)}
      >
        <NavigationControl position="top-right" showCompass={false} />
        {plottable.map((e) => {
          const tint = STATUS_TINT[e.worst];
          return (
            <Marker
              key={e.building.id}
              longitude={e.building.longitude!}
              latitude={e.building.latitude!}
              anchor="bottom"
              onClick={(ev) => {
                ev.originalEvent.stopPropagation();
                setOpenId(e.building.id);
              }}
            >
              <button
                type="button"
                aria-label={`${e.building.name} — ${tint.label}`}
                className={cn(
                  "grid h-8 w-8 -translate-y-2 place-items-center rounded-full text-xs font-semibold shadow-md ring-2 ring-white transition hover:scale-110",
                  tint.pin
                )}
              >
                {e.totals.total}
              </button>
            </Marker>
          );
        })}
        {openEntry && (
          <Popup
            longitude={openEntry.building.longitude!}
            latitude={openEntry.building.latitude!}
            anchor="top"
            offset={12}
            onClose={() => setOpenId(null)}
            closeButton={true}
            closeOnClick={false}
            className="[&_.mapboxgl-popup-content]:rounded-md [&_.mapboxgl-popup-content]:bg-card [&_.mapboxgl-popup-content]:text-foreground [&_.mapboxgl-popup-content]:shadow-lg [&_.mapboxgl-popup-content]:border [&_.mapboxgl-popup-content]:border-border [&_.mapboxgl-popup-tip]:!border-t-card"
          >
            <div className="min-w-[220px] space-y-2 p-1">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate text-sm font-semibold">
                    {openEntry.building.name}
                  </div>
                  {openEntry.building.address && (
                    <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
                      {openEntry.building.address}
                    </div>
                  )}
                </div>
                <span
                  className={cn(
                    "inline-flex shrink-0 items-center rounded-full px-2 py-0.5 text-[10px] font-semibold ring-1",
                    STATUS_TINT[openEntry.worst].pin,
                    STATUS_TINT[openEntry.worst].ring
                  )}
                >
                  {STATUS_TINT[openEntry.worst].label}
                </span>
              </div>
              <div className="grid grid-cols-4 gap-1 text-center text-[10px]">
                <MiniStat label="Total" value={openEntry.totals.total} />
                <MiniStat
                  label="Off"
                  value={openEntry.totals.offline}
                  tone={openEntry.totals.offline > 0 ? "danger" : undefined}
                />
                <MiniStat
                  label="Deg"
                  value={openEntry.totals.degraded}
                  tone={openEntry.totals.degraded > 0 ? "warn" : undefined}
                />
                <MiniStat label="On" value={openEntry.totals.online} tone="ok" />
              </div>
              <Link
                href={`/devices?building=${encodeURIComponent(openEntry.building.name)}`}
                className="block rounded-md border border-border bg-background px-2 py-1.5 text-center text-xs font-medium text-foreground transition-colors hover:bg-muted"
              >
                View devices →
              </Link>
            </div>
          </Popup>
        )}
      </Map>
    </div>
  );
}

function MiniStat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone?: "ok" | "warn" | "danger";
}) {
  return (
    <div
      className={cn(
        "rounded border px-1 py-0.5",
        tone === "danger" && "border-destructive/30 text-destructive",
        tone === "warn" && "border-warning/40 text-warning",
        tone === "ok" && "border-success/30 text-success",
        !tone && "border-border text-muted-foreground"
      )}
    >
      <div className="text-[9px] uppercase tracking-wider opacity-70">{label}</div>
      <div className="font-semibold text-foreground">{value}</div>
    </div>
  );
}
