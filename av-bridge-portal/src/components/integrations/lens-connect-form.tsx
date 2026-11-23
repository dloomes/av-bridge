"use client";

import { useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { apiMessage } from "@/lib/api-error";
import type { Integration, LensAccount } from "@/lib/types";

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

// Connect or update the Poly Lens integration. The account select is shown on
// first connect, or to vendor users; a customer editing an existing connection
// keeps the stored account (the API refuses an account change from them).
export function LensConnectForm({ integration, isVendor, onCancel, onSaved, onDisconnect, onDirty }: Props) {
  const hasAccount = !!integration?.account_id;
  const showAccount = !hasAccount || isVendor;
  const [clientId, setClientId] = useState(integration?.client_id ?? "");
  const [secret, setSecret] = useState("");
  const [accounts, setAccounts] = useState<LensAccount[] | null>(null);
  const [accountId, setAccountId] = useState("");
  const [tagging, setTagging] = useState(integration?.allow_tagging ?? false);
  const [busy, setBusy] = useState<"test" | "save" | null>(null);
  const [error, setError] = useState<string | null>(null);

  const touch = () => onDirty(true);
  const canTest = clientId.trim() !== "" && (secret !== "" || !!integration?.has_secret);

  async function runTest(): Promise<LensAccount[] | null> {
    setBusy("test");
    setError(null);
    try {
      const res = await api.testLens({ client_id: clientId.trim(), client_secret: secret || undefined });
      setAccounts(res.accounts);
      if (res.accounts.length === 0) {
        setError("These credentials work but no Poly Lens accounts are visible to them.");
        return null;
      }
      const keep = res.accounts.find((a) => a.id === integration?.account_id);
      const pick = keep ?? (res.accounts.length === 1 ? res.accounts[0] : undefined);
      setAccountId((cur) => cur || pick?.id || "");
      return res.accounts;
    } catch (e) {
      setError(apiMessage(e));
      return null;
    } finally {
      setBusy(null);
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    let chosen = accountId;
    if (showAccount && !chosen) {
      const found = await runTest();
      if (!found) return;
      const keep = found.find((a) => a.id === integration?.account_id);
      chosen = keep?.id ?? (found.length === 1 ? found[0].id : "");
      if (!chosen) {
        setError("Choose the Poly Lens account to use, then connect.");
        return;
      }
    }
    setBusy("save");
    setError(null);
    try {
      await api.saveLens({
        client_id: clientId.trim(),
        client_secret: secret || undefined,
        account_id: showAccount ? chosen : "",
        allow_tagging: tagging,
      });
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
      <p className="text-sm text-muted-foreground">
        In Poly Lens: Admin Portal → Account Settings → <span className="font-medium">API Credentials</span> → Add New.
        Paste the values here.
      </p>
      <div className="space-y-1">
        <label htmlFor="lens-client-id" className="text-xs font-medium text-muted-foreground">
          Client ID
        </label>
        <input
          id="lens-client-id"
          autoFocus
          autoComplete="off"
          value={clientId}
          onChange={(e) => {
            setClientId(e.target.value);
            touch();
          }}
          className={inputCls}
        />
      </div>
      <div className="space-y-1">
        <label htmlFor="lens-secret" className="text-xs font-medium text-muted-foreground">
          Client secret
        </label>
        <input
          id="lens-secret"
          type="password"
          autoComplete="new-password"
          value={secret}
          onChange={(e) => {
            setSecret(e.target.value);
            touch();
          }}
          placeholder={integration?.has_secret ? "Leave blank to keep the current secret" : ""}
          className={inputCls}
        />
      </div>

      {showAccount && (
        <div className="space-y-1">
          <div className="flex items-center justify-between gap-2">
            <label htmlFor="lens-account" className="text-xs font-medium text-muted-foreground">
              Lens account
            </label>
            <Button type="button" variant="outline" size="sm" disabled={!canTest || busy !== null} onClick={() => void runTest()}>
              {busy === "test" && <Loader2 className="h-4 w-4 animate-spin" />}
              Test
            </Button>
          </div>
          <select
            id="lens-account"
            value={accountId}
            disabled={!accounts || accounts.length === 0}
            onChange={(e) => {
              setAccountId(e.target.value);
              touch();
            }}
            className={inputCls}
          >
            <option value="">
              {accounts
                ? `${accounts.length} account${accounts.length === 1 ? "" : "s"} found: choose one`
                : "Press Test to list the accounts these credentials can see"}
            </option>
            {accounts?.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
          {hasAccount && (
            <p className="text-xs text-muted-foreground">
              Changing the account needs the client secret entered again.
            </p>
          )}
        </div>
      )}
      {!showAccount && (
        <p className="text-sm">
          <span className="text-xs font-medium text-muted-foreground">Lens account</span>
          <br />
          {integration?.account_name}
        </p>
      )}

      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={tagging}
          onChange={(e) => {
            setTagging(e.target.checked);
            touch();
          }}
          className="mt-0.5 h-4 w-4"
        />
        <span>Allow M.A.R.C.U.S. to tag linked devices in Lens</span>
      </label>

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
          <Button type="submit" disabled={busy !== null || clientId.trim() === ""}>
            {busy && <Loader2 className="h-4 w-4 animate-spin" />}
            Test and connect
          </Button>
        </div>
      </div>
    </form>
  );
}
