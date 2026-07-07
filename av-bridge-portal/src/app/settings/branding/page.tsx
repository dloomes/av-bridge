"use client";

import { useEffect, useRef, useState } from "react";
import { Loader2, Upload, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { useBranding } from "@/components/branding-provider";
import { api } from "@/lib/api";

// Byte cap on the incoming file. The backend enforces the same cap (see
// maxLogoBytes in branding.go) so anything larger reaches this check first
// and never leaves the browser.
const MAX_LOGO_BYTES = 256 * 1024;
const ALLOWED_TYPES = ["image/png", "image/jpeg", "image/svg+xml"];

// Read a File as a data URL. Used to convert the user's upload into the
// data:image/...;base64,... string the branding endpoint accepts. Wrapped
// in a promise so it composes with the async save handler.
function fileToDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(new Error("could not read file"));
    reader.readAsDataURL(file);
  });
}

export default function BrandingPage() {
  const { branding, refresh } = useBranding();
  const fileInput = useRef<HTMLInputElement>(null);

  // Local draft state — stays in sync with the live branding while the
  // user hasn't edited anything, then diverges once they touch a field.
  // Save button diffs against `branding` to build the PATCH.
  const [displayName, setDisplayName] = useState(branding.display_name ?? "");
  const [accentColor, setAccentColor] = useState(branding.accent_color ?? "#3b82f6");
  const [logoDataURL, setLogoDataURL] = useState(branding.logo_data_url ?? "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Reset the draft when the underlying branding changes (post-save or
  // scope switch for a vendor).
  useEffect(() => {
    setDisplayName(branding.display_name ?? "");
    setAccentColor(branding.accent_color ?? "#3b82f6");
    setLogoDataURL(branding.logo_data_url ?? "");
  }, [branding]);

  async function handleFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    setError(null);
    const file = e.target.files?.[0];
    if (!file) return;
    if (!ALLOWED_TYPES.includes(file.type)) {
      setError("Logo must be PNG, JPEG, or SVG.");
      e.target.value = "";
      return;
    }
    if (file.size > MAX_LOGO_BYTES) {
      setError(`Logo must be under ${MAX_LOGO_BYTES / 1024}KB.`);
      e.target.value = "";
      return;
    }
    try {
      const url = await fileToDataURL(file);
      setLogoDataURL(url);
    } catch {
      setError("Could not read the selected file.");
    }
    // Reset the input so choosing the same filename twice still fires
    // the onChange (useful when the user wants to re-upload after a clear).
    e.target.value = "";
  }

  function clearLogo() {
    setLogoDataURL("");
  }

  async function handleSave() {
    setSaving(true);
    setError(null);
    setSaved(false);
    // Diff — only send fields that changed. Distinguishes "unchanged"
    // (omit) from "clear" (empty string) as the API contract expects.
    const body: {
      display_name?: string;
      accent_color?: string;
      logo_data_url?: string;
    } = {};
    if (displayName !== (branding.display_name ?? "")) {
      body.display_name = displayName;
    }
    if (accentColor !== (branding.accent_color ?? "")) {
      body.accent_color = accentColor;
    }
    if (logoDataURL !== (branding.logo_data_url ?? "")) {
      body.logo_data_url = logoDataURL;
    }
    try {
      await api.updateBranding(body);
      refresh();
      setSaved(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "save failed");
    } finally {
      setSaving(false);
    }
  }

  const dirty =
    displayName !== (branding.display_name ?? "") ||
    accentColor !== (branding.accent_color ?? "") ||
    logoDataURL !== (branding.logo_data_url ?? "");

  return (
    <div className="flex flex-col h-screen">
      <header className="border-b bg-card/50 px-6 py-4">
        <h1 className="text-xl font-semibold">Branding</h1>
        <p className="text-sm text-muted-foreground">
          Customise how the portal presents to users in this tenant. Changes apply
          immediately after saving.
        </p>
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto px-6 py-6">
        <div className="max-w-3xl space-y-6">
          <Card>
            <CardContent className="space-y-6 p-6">
              <div className="space-y-2">
                <label
                  htmlFor="display_name"
                  className="text-sm font-medium leading-none"
                >
                  Product name
                </label>
                <p className="text-xs text-muted-foreground">
                  Overrides the default "AV Bridge" wordmark shown in the header
                  and browser tab. Leave blank to fall back to the default.
                </p>
                <input
                  id="display_name"
                  type="text"
                  value={displayName}
                  onChange={(e) => setDisplayName(e.target.value)}
                  placeholder="AV Bridge"
                  maxLength={64}
                  className="w-full max-w-sm rounded-md border bg-background px-3 py-2 text-sm"
                />
              </div>

              <div className="space-y-2">
                <label
                  htmlFor="accent_color"
                  className="text-sm font-medium leading-none"
                >
                  Accent colour
                </label>
                <p className="text-xs text-muted-foreground">
                  Applied to buttons, links, and highlights across the portal.
                </p>
                <div className="flex items-center gap-3">
                  <input
                    id="accent_color"
                    type="color"
                    value={accentColor}
                    onChange={(e) => setAccentColor(e.target.value)}
                    className="h-10 w-16 cursor-pointer rounded border bg-background p-1"
                  />
                  <input
                    type="text"
                    value={accentColor}
                    onChange={(e) => setAccentColor(e.target.value)}
                    placeholder="#3b82f6"
                    className="w-32 rounded-md border bg-background px-3 py-2 text-sm font-mono"
                  />
                </div>
              </div>

              <div className="space-y-2">
                <label className="text-sm font-medium leading-none">Logo</label>
                <p className="text-xs text-muted-foreground">
                  PNG, JPEG, or SVG. Max{" "}
                  {(MAX_LOGO_BYTES / 1024).toFixed(0)}KB. Shown in the sidebar and
                  page headers.
                </p>
                <div className="flex items-center gap-4">
                  {logoDataURL ? (
                    <img
                      src={logoDataURL}
                      alt=""
                      className="h-16 w-16 rounded object-contain border bg-muted"
                    />
                  ) : (
                    <div className="h-16 w-16 rounded border border-dashed flex items-center justify-center text-xs text-muted-foreground">
                      none
                    </div>
                  )}
                  <div className="flex flex-col gap-2">
                    <input
                      ref={fileInput}
                      type="file"
                      accept={ALLOWED_TYPES.join(",")}
                      onChange={handleFileChange}
                      className="hidden"
                    />
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => fileInput.current?.click()}
                    >
                      <Upload className="h-3.5 w-3.5" />
                      {logoDataURL ? "Replace" : "Upload"}
                    </Button>
                    {logoDataURL && (
                      <Button variant="ghost" size="sm" onClick={clearLogo}>
                        <X className="h-3.5 w-3.5" />
                        Remove
                      </Button>
                    )}
                  </div>
                </div>
              </div>

              {error && (
                <div className="text-sm [color:hsl(var(--destructive))]">
                  {error}
                </div>
              )}
              {saved && !error && !dirty && (
                <div className="text-sm text-muted-foreground">Saved.</div>
              )}

              <div className="flex items-center justify-end gap-2 pt-2 border-t">
                <Button
                  size="sm"
                  onClick={handleSave}
                  disabled={!dirty || saving}
                >
                  {saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                  Save changes
                </Button>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardContent className="p-6 space-y-2">
              <h2 className="text-sm font-semibold">Preview</h2>
              <p className="text-xs text-muted-foreground">
                The sidebar and page headers update as soon as you save. This
                card shows what the branding looks like right now — the tab title
                also switches to "{displayName || "AV Bridge"}".
              </p>
              <div className="mt-4 flex items-center gap-3 rounded-md border bg-muted/30 p-4">
                {logoDataURL ? (
                  <img
                    src={logoDataURL}
                    alt=""
                    className="h-10 w-10 rounded object-contain"
                  />
                ) : (
                  <div className="h-10 w-10 rounded bg-primary/20" />
                )}
                <div className="flex-1">
                  <div className="text-lg font-semibold">
                    {displayName || "AV Bridge"}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    Sample header
                  </div>
                </div>
                <button
                  type="button"
                  className="rounded-md px-3 py-1.5 text-sm text-white"
                  style={{ backgroundColor: accentColor }}
                >
                  Accent
                </button>
              </div>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
