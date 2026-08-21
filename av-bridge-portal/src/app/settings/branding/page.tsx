"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { ArrowLeft, Loader2, Upload, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { useBranding } from "@/components/branding-provider";
import { useSession } from "@/hooks/useSession";
import { setScope } from "@/lib/session";
import { api } from "@/lib/api";

// Byte caps on the two image uploads. Backend enforces the same caps
// (maxLogoBytes / maxHeroBytes in branding.go) so oversized files never
// leave the browser.
const MAX_LOGO_BYTES = 256 * 1024;
const MAX_HERO_BYTES = 1024 * 1024;
const ALLOWED_LOGO_TYPES = ["image/png", "image/jpeg", "image/svg+xml"];
const ALLOWED_HERO_TYPES = ["image/png", "image/jpeg", "image/svg+xml", "image/webp"];

// Length caps mirror the backend CHECK constraints.
const MAX_SIGN_IN_MESSAGE = 500;
const MAX_SUPPORT_CONTACT = 200;
const MAX_SSO_BUTTON_LABEL = 60;

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
  const session = useSession();
  const router = useRouter();
  const logoInput = useRef<HTMLInputElement>(null);
  const heroInput = useRef<HTMLInputElement>(null);

  // Local draft state — stays in sync with the live branding while the
  // user hasn't edited anything, then diverges once they touch a field.
  // Save button diffs against `branding` to build the PATCH.
  const [displayName, setDisplayName] = useState(branding.display_name ?? "");
  const [accentColor, setAccentColor] = useState(branding.accent_color ?? "#3b82f6");
  const [logoDataURL, setLogoDataURL] = useState(branding.logo_data_url ?? "");
  const [signInMessage, setSignInMessage] = useState(branding.sign_in_message ?? "");
  const [supportContact, setSupportContact] = useState(branding.support_contact ?? "");
  const [ssoButtonLabel, setSsoButtonLabel] = useState(branding.sso_button_label ?? "");
  const [heroDataURL, setHeroDataURL] = useState(branding.sign_in_hero_data_url ?? "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Reset the draft when the underlying branding changes (post-save or
  // scope switch for a vendor).
  useEffect(() => {
    setDisplayName(branding.display_name ?? "");
    setAccentColor(branding.accent_color ?? "#3b82f6");
    setLogoDataURL(branding.logo_data_url ?? "");
    setSignInMessage(branding.sign_in_message ?? "");
    setSupportContact(branding.support_contact ?? "");
    setSsoButtonLabel(branding.sso_button_label ?? "");
    setHeroDataURL(branding.sign_in_hero_data_url ?? "");
  }, [branding]);

  // A vendor session with a customer scope means "editing this customer's
  // branding on their behalf" — show a banner and a fast route back.
  const editingCustomerAsVendor =
    !!session.user?.is_vendor && !!session.scope;
  const backToCustomers = () => {
    setScope(null);
    router.push("/customers");
  };

  // Shared file-picker handler — used by both the logo and hero uploads.
  // Different byte caps + type allowlists per surface; kept as one function
  // so behaviour stays uniform (read → set state → reset input).
  async function handleImageChange(
    e: React.ChangeEvent<HTMLInputElement>,
    opts: {
      allowedTypes: string[];
      maxBytes: number;
      label: string;
      set: (url: string) => void;
    }
  ) {
    setError(null);
    const file = e.target.files?.[0];
    if (!file) return;
    if (!opts.allowedTypes.includes(file.type)) {
      setError(`${opts.label} must be one of: ${opts.allowedTypes.map((t) => t.split("/")[1]).join(", ")}.`);
      e.target.value = "";
      return;
    }
    if (file.size > opts.maxBytes) {
      setError(`${opts.label} must be under ${Math.round(opts.maxBytes / 1024)}KB.`);
      e.target.value = "";
      return;
    }
    try {
      const url = await fileToDataURL(file);
      opts.set(url);
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
  function clearHero() {
    setHeroDataURL("");
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
      sign_in_message?: string;
      support_contact?: string;
      sso_button_label?: string;
      sign_in_hero_data_url?: string;
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
    if (signInMessage !== (branding.sign_in_message ?? "")) {
      body.sign_in_message = signInMessage;
    }
    if (supportContact !== (branding.support_contact ?? "")) {
      body.support_contact = supportContact;
    }
    if (ssoButtonLabel !== (branding.sso_button_label ?? "")) {
      body.sso_button_label = ssoButtonLabel;
    }
    if (heroDataURL !== (branding.sign_in_hero_data_url ?? "")) {
      body.sign_in_hero_data_url = heroDataURL;
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
    logoDataURL !== (branding.logo_data_url ?? "") ||
    signInMessage !== (branding.sign_in_message ?? "") ||
    supportContact !== (branding.support_contact ?? "") ||
    ssoButtonLabel !== (branding.sso_button_label ?? "") ||
    heroDataURL !== (branding.sign_in_hero_data_url ?? "");

  return (
    <div className="flex flex-col h-screen">
      <header className="border-b bg-card/50 px-6 py-4">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h1 className="text-xl font-semibold">Branding</h1>
            <p className="text-sm text-muted-foreground">
              Customise how the portal presents to users in this tenant. Changes apply
              immediately after saving.
            </p>
          </div>
          {editingCustomerAsVendor && (
            <Button variant="outline" size="sm" onClick={backToCustomers}>
              <ArrowLeft className="h-3.5 w-3.5" />
              Back to customers
            </Button>
          )}
        </div>
        {editingCustomerAsVendor && (
          <div className="mt-3 rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-xs [color:hsl(var(--warning))]">
            You&rsquo;re editing branding on behalf of a customer. Anything you save here appears in <em>their</em> sign-in page and portal chrome.
          </div>
        )}
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
                      ref={logoInput}
                      type="file"
                      accept={ALLOWED_LOGO_TYPES.join(",")}
                      onChange={(e) =>
                        handleImageChange(e, {
                          allowedTypes: ALLOWED_LOGO_TYPES,
                          maxBytes: MAX_LOGO_BYTES,
                          label: "Logo",
                          set: setLogoDataURL,
                        })
                      }
                      className="hidden"
                    />
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => logoInput.current?.click()}
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

            </CardContent>
          </Card>

          <Card>
            <CardContent className="space-y-6 p-6">
              <div>
                <h2 className="text-sm font-semibold">Sign-in page</h2>
                <p className="text-xs text-muted-foreground mt-0.5">
                  These fields render on the customer&rsquo;s branded sign-in page at{" "}
                  <span className="font-mono">&lt;slug&gt;.uat.involvecloud.com</span>{" "}
                  before the user has signed in.
                </p>
              </div>

              <div className="space-y-2">
                <label htmlFor="sign_in_message" className="text-sm font-medium leading-none">
                  Welcome message
                </label>
                <p className="text-xs text-muted-foreground">
                  Short line shown under the product name. Great for &ldquo;Welcome
                  to Acme&rsquo;s ops portal&rdquo; or a note about who to contact.
                </p>
                <textarea
                  id="sign_in_message"
                  value={signInMessage}
                  onChange={(e) => setSignInMessage(e.target.value.slice(0, MAX_SIGN_IN_MESSAGE))}
                  placeholder="Welcome. Sign in to manage rooms and devices."
                  maxLength={MAX_SIGN_IN_MESSAGE}
                  rows={2}
                  className="w-full rounded-md border bg-background px-3 py-2 text-sm resize-none"
                />
                <div className="text-[11px] text-muted-foreground text-right">
                  {signInMessage.length} / {MAX_SIGN_IN_MESSAGE}
                </div>
              </div>

              <div className="space-y-2">
                <label htmlFor="support_contact" className="text-sm font-medium leading-none">
                  Support contact
                </label>
                <p className="text-xs text-muted-foreground">
                  Email or URL shown in the sign-in footer. Users stuck at the
                  login screen have somewhere to turn.
                </p>
                <input
                  id="support_contact"
                  type="text"
                  value={supportContact}
                  onChange={(e) => setSupportContact(e.target.value.slice(0, MAX_SUPPORT_CONTACT))}
                  placeholder="acme.support@involve.vc"
                  maxLength={MAX_SUPPORT_CONTACT}
                  className="w-full max-w-md rounded-md border bg-background px-3 py-2 text-sm"
                />
              </div>

              <div className="space-y-2">
                <label htmlFor="sso_button_label" className="text-sm font-medium leading-none">
                  SSO button label
                </label>
                <p className="text-xs text-muted-foreground">
                  Overrides the default &ldquo;Sign in with Microsoft&rdquo; text on
                  the Entra CTA. Leave blank to keep the default. Only visible if
                  the tenant is federated.
                </p>
                <input
                  id="sso_button_label"
                  type="text"
                  value={ssoButtonLabel}
                  onChange={(e) => setSsoButtonLabel(e.target.value.slice(0, MAX_SSO_BUTTON_LABEL))}
                  placeholder="Sign in with Microsoft"
                  maxLength={MAX_SSO_BUTTON_LABEL}
                  className="w-full max-w-md rounded-md border bg-background px-3 py-2 text-sm"
                />
              </div>

              <div className="space-y-2">
                <label className="text-sm font-medium leading-none">
                  Hero background image
                </label>
                <p className="text-xs text-muted-foreground">
                  PNG, JPEG, SVG, or WebP. Max{" "}
                  {(MAX_HERO_BYTES / 1024).toFixed(0)}KB. Fills the left half of
                  the sign-in page on wide screens; ignored on narrow screens.
                </p>
                <div className="flex items-start gap-4">
                  {heroDataURL ? (
                    <img
                      src={heroDataURL}
                      alt=""
                      className="h-24 w-40 rounded object-cover border bg-muted"
                    />
                  ) : (
                    <div className="h-24 w-40 rounded border border-dashed flex items-center justify-center text-xs text-muted-foreground">
                      none
                    </div>
                  )}
                  <div className="flex flex-col gap-2">
                    <input
                      ref={heroInput}
                      type="file"
                      accept={ALLOWED_HERO_TYPES.join(",")}
                      onChange={(e) =>
                        handleImageChange(e, {
                          allowedTypes: ALLOWED_HERO_TYPES,
                          maxBytes: MAX_HERO_BYTES,
                          label: "Hero image",
                          set: setHeroDataURL,
                        })
                      }
                      className="hidden"
                    />
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => heroInput.current?.click()}
                    >
                      <Upload className="h-3.5 w-3.5" />
                      {heroDataURL ? "Replace" : "Upload"}
                    </Button>
                    {heroDataURL && (
                      <Button variant="ghost" size="sm" onClick={clearHero}>
                        <X className="h-3.5 w-3.5" />
                        Remove
                      </Button>
                    )}
                  </div>
                </div>
              </div>
            </CardContent>
          </Card>

          {(error || (saved && !error && !dirty)) && (
            <div className="text-sm">
              {error ? (
                <span className="[color:hsl(var(--destructive))]">{error}</span>
              ) : (
                <span className="text-muted-foreground">Saved.</span>
              )}
            </div>
          )}
          <div className="flex items-center justify-end gap-2">
            <Button
              size="sm"
              onClick={handleSave}
              disabled={!dirty || saving}
            >
              {saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              Save changes
            </Button>
          </div>

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
