"use client";

import { useEffect, useState } from "react";
import { AlertTriangle, Download, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/modal";
import { useToast } from "@/components/toast";
import { api, type HelpdeskCustomer } from "@/lib/api";
import { plainError, saveDownload } from "@/lib/download";
import { daysUntil, formatDay, sameCustomerName } from "@/lib/offboarding";

const inputCls =
  "w-full rounded-md border bg-background px-3 py-2 text-sm disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function ErrorLine({ text }: { text: string }) {
  return (
    <div
      role="alert"
      className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm [color:hsl(var(--destructive))]"
    >
      {text}
    </div>
  );
}

// OffboardDialog: explains what happens, and only enables the button once the
// customer's name has been typed exactly.
export function OffboardDialog({
  customer,
  onClose,
  onDone,
}: {
  customer: HelpdeskCustomer | null;
  onClose: () => void;
  onDone: () => void;
}) {
  // While a request is running, closing is a no-op (X, Esc and click-outside
  // included), so a late result can't land on a different customer's dialog.
  const [busy, setBusy] = useState(false);
  return (
    <Modal
      open={customer !== null}
      onClose={() => {
        if (!busy) onClose();
      }}
      title="Offboard customer"
      wide={false}
    >
      {customer && (
        <OffboardForm
          key={customer.id}
          customer={customer}
          onClose={onClose}
          onDone={onDone}
          onBusy={setBusy}
        />
      )}
    </Modal>
  );
}

function OffboardForm({
  customer,
  onClose,
  onDone,
  onBusy,
}: {
  customer: HelpdeskCustomer;
  onClose: () => void;
  onDone: () => void;
  onBusy: (b: boolean) => void;
}) {
  const { toast } = useToast();
  const [typed, setTyped] = useState("");
  const [busy, setBusyState] = useState(false);
  const setBusy = (b: boolean) => {
    setBusyState(b);
    onBusy(b);
  };
  // If the form goes away mid-request, don't leave the dialog locked.
  useEffect(() => () => onBusy(false), []); // eslint-disable-line react-hooks/exhaustive-deps
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (busy || !sameCustomerName(typed, customer.name)) return;
    setBusy(true);
    setError(null);
    try {
      const r = await api.offboardCustomer(customer.id, typed.trim());
      toast({
        title: `${customer.name} will be deleted on ${formatDay(r.delete_after)}`,
        variant: "success",
      });
      setBusy(false);
      onDone();
    } catch (err) {
      setError(plainError(err));
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <p className="text-sm">
        You are about to offboard <span className="font-semibold break-words">{customer.name}</span>.
      </p>
      <div className="space-y-2 text-sm">
        <p>
          <span className="font-medium">Today:</span> everyone at this customer is signed out and
          can&apos;t sign in again. Their access tokens and Collectors stop working, and
          monitoring and alerts stop. Nothing is deleted yet.
        </p>
        <p>
          <span className="font-medium">In 30 days:</span> all of the customer&apos;s data is
          permanently deleted. This can&apos;t be undone. A deletion certificate is kept.
        </p>
        <p>
          Before then you can download a copy of the customer&apos;s data, or cancel and
          everything works again.
        </p>
      </div>
      <div className="space-y-1">
        <label htmlFor="offboard-name" className="text-xs font-medium text-muted-foreground">
          Type the customer name exactly to confirm
        </label>
        <input
          id="offboard-name"
          autoComplete="off"
          disabled={busy}
          value={typed}
          onChange={(e) => {
            setTyped(e.target.value);
            setError(null);
          }}
          placeholder={customer.name}
          className={inputCls}
        />
        <p className="text-[11px] text-muted-foreground">Capital letters must match.</p>
      </div>
      {error && <ErrorLine text={error} />}
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
          Keep customer
        </Button>
        <Button
          type="submit"
          variant="destructive"
          disabled={busy || !sameCustomerName(typed, customer.name)}
        >
          {busy && <Loader2 className="h-4 w-4 animate-spin" />}
          Offboard customer
        </Button>
      </div>
    </form>
  );
}

// OffboardingBanner: shown on a customer that is offboarding.
export function OffboardingBanner({
  customer,
  onChanged,
}: {
  customer: HelpdeskCustomer;
  onChanged: () => void;
}) {
  const { toast } = useToast();
  const [downloading, setDownloading] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [error, setError] = useState<string | null>(null);
  if (!customer.delete_after) return null;
  const days = daysUntil(customer.delete_after);

  const download = async () => {
    if (downloading) return;
    setDownloading(true);
    setError(null);
    try {
      const d = await api.downloadCustomerExport(customer.id);
      saveDownload(d);
      toast({ title: `Downloaded ${d.filename}`, variant: "success" });
    } catch (err) {
      setError(plainError(err));
    } finally {
      setDownloading(false);
    }
  };

  const cancel = async () => {
    if (cancelling) return;
    setCancelling(true);
    setError(null);
    try {
      await api.cancelOffboarding(customer.id);
      toast({ title: `Offboarding cancelled for ${customer.name}`, variant: "success" });
      setConfirming(false);
      onChanged();
    } catch (err) {
      setError(plainError(err));
      setConfirming(false);
    } finally {
      setCancelling(false);
    }
  };

  return (
    <div className="space-y-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-sm text-amber-800 dark:text-amber-200">
      <p className="flex items-start gap-1.5 font-medium">
        <AlertTriangle aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0" />
        <span>
          Offboarding: deleted on {formatDay(customer.delete_after)} ({days}{" "}
          {days === 1 ? "day" : "days"})
        </span>
      </p>
      {error && <ErrorLine text={error} />}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="outline" onClick={download} disabled={downloading}>
          {downloading ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Download className="h-3.5 w-3.5" />
          )}
          Download export
        </Button>
        <Button size="sm" variant="outline" onClick={() => setConfirming(true)}>
          Cancel offboarding
        </Button>
      </div>

      <Modal
        open={confirming}
        onClose={() => !cancelling && setConfirming(false)}
        title="Cancel offboarding"
        wide={false}
      >
        <div className="space-y-4 text-foreground">
          <p className="text-sm">
            Cancel the offboarding of <span className="font-semibold break-words">{customer.name}</span>?
            Nothing has been deleted, so everything works again. People will need to sign in again.
          </p>
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="ghost" onClick={() => setConfirming(false)} disabled={cancelling}>
              Keep offboarding
            </Button>
            <Button onClick={cancel} disabled={cancelling}>
              {cancelling && <Loader2 className="h-4 w-4 animate-spin" />}
              Cancel offboarding
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
