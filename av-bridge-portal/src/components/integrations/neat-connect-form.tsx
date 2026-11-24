"use client";

import { useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { apiMessage } from "@/lib/api-error";
import type { Integration } from "@/lib/types";

const inputCls =
  "w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

interface Props {
  integration: Integration | null;
  isVendor: boolean;
  onCancel: () => void;
  onSaved: () => void;
  onDisconnect: () => void;
  onDirty: (dirty: boolean) => void;
}

// Connect or update the Neat Pulse integration (read-only). The organisation
// ID can be typed on first connect, or by vendor users; a customer editing an
// existing connection keeps the stored organisation (the API refuses a change
// from them), and changing it always needs the API key again.
export function NeatConnectForm({ integration, isVendor, onCancel, onSaved, onDisconnect, onDirty }: Props) {
  const hasOrg = !!integration?.account_id;
  const canEditOrg = !hasOrg || isVendor;
  const [orgId, setOrgId] = useState(integration?.account_id ?? "");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState<"test" | "save" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tested, setTested] = useState<string | null>(null);

  const touch = () => {
    setTested(null);
    onDirty(true);
  };
  const orgChanged = hasOrg && orgId.trim() !== (integration?.account_id ?? "");
  const keyNeeded = !integration?.has_secret || orgChanged || !hasOrg;
  const canTest = orgId.trim() !== "" && (apiKey !== "" || (isVendor && !!integration?.has_secret && !orgChanged));

  async function runTest() {
    setBusy("test");
    setError(null);
    try {
      const res = await api.testNeat({ org_id: orgId.trim(), api_key: apiKey || undefined });
      setTested(`Connected: ${res.device_count} ${res.device_count === 1 ? "device" : "devices"} in this organisation.`);
    } catch (e) {
      setError(apiMessage(e));
    } finally {
      setBusy(null);
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    if (orgId.trim() === "") {
      setError("Enter the Neat Pulse organisation ID.");
      return;
    }
    if (keyNeeded && apiKey === "") {
      setError(orgChanged ? "Enter the API key again to change the organisation." : "Enter the API key.");
      return;
    }
    setBusy("save");
    setError(null);
    try {
      await api.saveNeat({ org_id: orgId.trim(), api_key: apiKey || undefined });
      onDirty(false);
      onSaved();
    } catch (err) {
      setError(apiMessage(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      {/* Unverified wording: the Pulse menu path and where the organisation ID
          appears were not checked against a live Pulse tenant. */}
      <p className="text-sm text-muted-foreground">
        In Neat Pulse: Settings → <span className="font-medium">API keys</span> → create a key with{" "}
        <span className="font-medium">Read</span> access. Your organisation ID is in the Pulse URL or settings.
        M.A.R.C.U.S. only reads from Pulse.
      </p>
      <div className="space-y-1">
        <label htmlFor="neat-org" className="text-xs font-medium text-muted-foreground">
          Organisation ID
        </label>
        <input
          id="neat-org"
          autoFocus={canEditOrg}
          autoComplete="off"
          value={orgId}
          disabled={!canEditOrg}
          onChange={(e) => {
            setOrgId(e.target.value);
            touch();
          }}
          className={inputCls}
        />
        {hasOrg && !isVendor && (
          <p className="text-xs text-muted-foreground">Only Involve support can change the organisation.</p>
        )}
        {hasOrg && isVendor && (
          <p className="text-xs text-muted-foreground">Changing the organisation needs the API key entered again.</p>
        )}
      </div>
      <div className="space-y-1">
        <div className="flex items-center justify-between gap-2">
          <label htmlFor="neat-key" className="text-xs font-medium text-muted-foreground">
            API key
          </label>
          <Button type="button" variant="outline" size="sm" disabled={!canTest || busy !== null} onClick={() => void runTest()}>
            {busy === "test" && <Loader2 className="h-4 w-4 animate-spin" />}
            Test
          </Button>
        </div>
        <input
          id="neat-key"
          type="password"
          autoFocus={!canEditOrg}
          autoComplete="new-password"
          value={apiKey}
          onChange={(e) => {
            setApiKey(e.target.value);
            touch();
          }}
          placeholder={integration?.has_secret && !orgChanged ? "Leave blank to keep the current key" : ""}
          className={inputCls}
        />
      </div>

      {tested && <p className="text-sm [color:hsl(var(--success))]">{tested}</p>}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
        <div>
          {integration && integration.status !== "disconnected" && integration.status !== "not_connected" && (
            <Button type="button" variant="ghost" className="text-destructive" onClick={onDisconnect} disabled={busy !== null}>
              Disconnect
            </Button>
          )}
        </div>
        <div className="flex gap-2">
          <Button type="button" variant="outline" onClick={onCancel} disabled={busy !== null}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy !== null || orgId.trim() === ""}>
            {busy === "save" && <Loader2 className="h-4 w-4 animate-spin" />}
            Test and connect
          </Button>
        </div>
      </div>
    </form>
  );
}
