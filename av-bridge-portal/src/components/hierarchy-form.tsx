"use client";

import { useState } from "react";
import { Loader2, MapPin } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { NamedRow } from "@/lib/types";

export type HierarchyKind = "region" | "location" | "building" | "room";
export type HierarchyMode = "create" | "edit";

// Initial values used in edit mode. Building entries can also carry
// address, timezone, and optional coords; the form ignores those fields
// for other kinds.
export interface HierarchyEditInitial {
  id: string;
  name: string;
  address?: string;
  timezone?: string;
  latitude?: number;
  longitude?: number;
}

const MAPBOX_TOKEN = process.env.NEXT_PUBLIC_MAPBOX_TOKEN ?? "";

interface HierarchyFormProps {
  kind: HierarchyKind;
  mode?: HierarchyMode;
  // Edit: pre-fill the form with current values.
  initial?: HierarchyEditInitial;
  // Create: parent id required for everything except "region". The page
  // passes it pre-filled so the operator never picks the wrong parent.
  parentId?: string;
  parentLabel?: string;
  onCancel: () => void;
  onSuccess: (saved: NamedRow) => void;
}

const labelClass =
  "text-xs font-medium text-muted-foreground uppercase tracking-wide";
const inputClass =
  "h-9 w-full rounded-md border border-input bg-background px-3 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function HierarchyForm({
  kind,
  mode = "create",
  initial,
  parentId,
  parentLabel,
  onCancel,
  onSuccess,
}: HierarchyFormProps) {
  const [name, setName] = useState(initial?.name ?? "");
  const [address, setAddress] = useState(initial?.address ?? "");
  const [timezone, setTimezone] = useState(initial?.timezone ?? "");
  // Coords are strings in the form so the user can clear them independently
  // and so partially-typed input (e.g. a lone minus sign) doesn't fight
  // React's controlled number inputs. Parsed to floats on submit.
  const [latitude, setLatitude] = useState(
    typeof initial?.latitude === "number" ? String(initial.latitude) : ""
  );
  const [longitude, setLongitude] = useState(
    typeof initial?.longitude === "number" ? String(initial.longitude) : ""
  );
  const [geocoding, setGeocoding] = useState(false);
  const [geocodeError, setGeocodeError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isEdit = mode === "edit";
  const needsParent = !isEdit && kind !== "region";

  // Parse the two coord inputs into a wire-ready payload. Rejects
  // partial pairs (one filled, one blank) so the backend never sees an
  // asymmetric value. Returns { coords: null } when both blank and the
  // caller can decide whether to clear or leave alone.
  const parseCoords = (): {
    ok: boolean;
    lat?: number;
    lon?: number;
    both_blank: boolean;
    err?: string;
  } => {
    const latStr = latitude.trim();
    const lonStr = longitude.trim();
    if (!latStr && !lonStr) return { ok: true, both_blank: true };
    if (!latStr || !lonStr) {
      return {
        ok: false,
        both_blank: false,
        err: "Enter both latitude and longitude, or clear both.",
      };
    }
    const lat = Number(latStr);
    const lon = Number(lonStr);
    if (!Number.isFinite(lat) || !Number.isFinite(lon)) {
      return { ok: false, both_blank: false, err: "Coordinates must be numeric." };
    }
    if (lat < -90 || lat > 90 || lon < -180 || lon > 180) {
      return {
        ok: false,
        both_blank: false,
        err: "Latitude must be -90..90 and longitude -180..180.",
      };
    }
    return { ok: true, both_blank: false, lat, lon };
  };

  const handleGeocode = async () => {
    if (!MAPBOX_TOKEN) {
      setGeocodeError("Mapbox token not configured — enter coords manually.");
      return;
    }
    if (!address.trim()) {
      setGeocodeError("Enter an address first.");
      return;
    }
    setGeocodeError(null);
    setGeocoding(true);
    try {
      // Mapbox Geocoding v5. limit=1 gives us the best-match feature;
      // types=address,poi keeps us on the "specific place" side rather
      // than region-level matches, which would drop a pin somewhere
      // useless like the middle of a city.
      const url =
        `https://api.mapbox.com/geocoding/v5/mapbox.places/${encodeURIComponent(address.trim())}.json` +
        `?access_token=${encodeURIComponent(MAPBOX_TOKEN)}&limit=1&types=address,poi`;
      const res = await fetch(url);
      if (!res.ok) throw new Error(`Mapbox returned ${res.status}`);
      const body = (await res.json()) as {
        features: Array<{ center: [number, number] }>;
      };
      const hit = body.features?.[0];
      if (!hit) throw new Error("No match for that address.");
      const [lon, lat] = hit.center;
      setLatitude(lat.toFixed(6));
      setLongitude(lon.toFixed(6));
    } catch (e) {
      setGeocodeError((e as Error).message);
    } finally {
      setGeocoding(false);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setError("Name is required");
      return;
    }
    if (needsParent && !parentId) {
      setError("Parent is missing");
      return;
    }
    setError(null);
    setSubmitting(true);
    // Coord validation only matters for buildings; other kinds ignore it.
    const coords = kind === "building" ? parseCoords() : { ok: true, both_blank: true };
    if (!coords.ok) {
      setError(coords.err ?? "Invalid coordinates");
      setSubmitting(false);
      return;
    }
    try {
      let saved: { id: string; name?: string };
      if (isEdit) {
        if (!initial) throw new Error("missing initial value for edit");
        switch (kind) {
          case "region":
            saved = await api.updateRegion(initial.id, name.trim());
            break;
          case "location":
            saved = await api.updateLocation(initial.id, name.trim());
            break;
          case "building": {
            // On edit we need to be explicit about coord intent: user
            // wiped both fields = clear the stored pair; both filled =
            // update; nothing typed and nothing pre-filled = leave alone.
            const hadCoords =
              typeof initial.latitude === "number" &&
              typeof initial.longitude === "number";
            const buildingBody: {
              name: string;
              address: string;
              timezone: string;
              latitude?: number;
              longitude?: number;
              clear_coords?: boolean;
            } = {
              name: name.trim(),
              address: address.trim(),
              timezone: timezone.trim(),
            };
            if (coords.both_blank) {
              if (hadCoords) buildingBody.clear_coords = true;
            } else {
              buildingBody.latitude = coords.lat;
              buildingBody.longitude = coords.lon;
            }
            saved = await api.updateBuilding(initial.id, buildingBody);
            break;
          }
          case "room":
            saved = await api.updateRoom(initial.id, name.trim());
            break;
        }
        onSuccess({
          id: initial.id,
          name: name.trim(),
          parent_id: parentId,
        });
      } else {
        switch (kind) {
          case "region":
            saved = await api.createRegion(name.trim());
            break;
          case "location":
            saved = await api.createLocation(parentId!, name.trim());
            break;
          case "building":
            saved = await api.createBuilding({
              location_id: parentId!,
              name: name.trim(),
              address: address.trim() || undefined,
              timezone: timezone.trim() || undefined,
              latitude: coords.both_blank ? undefined : coords.lat,
              longitude: coords.both_blank ? undefined : coords.lon,
            });
            break;
          case "room":
            saved = await api.createRoom(parentId!, name.trim());
            break;
        }
        onSuccess({
          id: saved.id,
          name: saved.name ?? name.trim(),
          parent_id: parentId,
        });
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]">
          {error}
        </div>
      )}

      {needsParent && parentLabel && (
        <div className="rounded-md border bg-muted/30 px-3 py-2 text-xs">
          <span className="text-muted-foreground">In </span>
          <span className="font-medium">{parentLabel}</span>
        </div>
      )}

      <div>
        <label className={labelClass}>Name</label>
        <input
          className={inputClass}
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoFocus
          required
        />
      </div>

      {kind === "building" && (
        <>
          <div>
            <label className={labelClass}>Address (optional)</label>
            <input
              className={inputClass}
              value={address}
              onChange={(e) => setAddress(e.target.value)}
              placeholder="123 Example Street"
            />
          </div>
          <div>
            <label className={labelClass}>Timezone (optional)</label>
            <input
              className={inputClass}
              value={timezone}
              onChange={(e) => setTimezone(e.target.value)}
              placeholder="Europe/London"
            />
          </div>
          <div className="space-y-2 rounded-md border border-border bg-muted/20 p-3">
            <div className="flex items-center justify-between">
              <label className={labelClass}>Map coordinates (optional)</label>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={handleGeocode}
                disabled={geocoding || !address.trim()}
              >
                {geocoding ? (
                  <Loader2 className="h-3 w-3 animate-spin" />
                ) : (
                  <MapPin className="h-3 w-3" />
                )}
                Geocode from address
              </Button>
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div>
                <label className="text-[10px] uppercase tracking-wide text-muted-foreground">
                  Latitude
                </label>
                <input
                  className={inputClass}
                  value={latitude}
                  onChange={(e) => setLatitude(e.target.value)}
                  placeholder="51.5074"
                  inputMode="decimal"
                />
              </div>
              <div>
                <label className="text-[10px] uppercase tracking-wide text-muted-foreground">
                  Longitude
                </label>
                <input
                  className={inputClass}
                  value={longitude}
                  onChange={(e) => setLongitude(e.target.value)}
                  placeholder="-0.1278"
                  inputMode="decimal"
                />
              </div>
            </div>
            {geocodeError && (
              <div className="text-xs [color:hsl(var(--destructive))]">
                {geocodeError}
              </div>
            )}
            <p className="text-[11px] text-muted-foreground">
              Buildings with coordinates appear as pins on the Map view.
              Leave both blank to hide from the map.
            </p>
          </div>
        </>
      )}

      <div className="flex items-center justify-end gap-2 pt-2 border-t">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting}>
          {submitting && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          {isEdit ? "Save changes" : `Create ${kind}`}
        </Button>
      </div>
    </form>
  );
}
