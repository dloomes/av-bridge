"use client";

// SetAsDefaultToggle — the tiny "make this page my sign-in landing"
// affordance that sits in the Overview and Map page headers. Reads
// the current landing_page from the session, PATCHes /me/preferences
// on click, and rewrites the local session so future signIn/whoami
// hydrates arrive already agreeing with the server.
//
// Kept dead-simple: one button. When you're already on your default,
// it renders the "star filled" state as a static badge — clicking it
// again would be a no-op so we don't bother.

import { useState } from "react";
import { Star, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { useSession } from "@/hooks/useSession";
import { signIn } from "@/lib/session";
import { useToast } from "@/components/toast";
import { cn } from "@/lib/utils";

interface Props {
  page: "overview" | "map";
  className?: string;
}

export function SetAsDefaultToggle({ page, className }: Props) {
  const session = useSession();
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);

  const current = session.user?.landing_page ?? "overview";
  const isDefault = current === page;

  if (!session.hydrated || !session.token) return null;

  const onClick = async () => {
    if (isDefault || busy) return;
    setBusy(true);
    try {
      await api.updatePreferences({ landing_page: page });
      // Rewrite the local session so a page reload without a whoami
      // still lands on the right page next time. We keep every other
      // field intact.
      if (session.user && session.token) {
        signIn(session.token, { ...session.user, landing_page: page });
      }
      toast({
        variant: "success",
        title:
          page === "map"
            ? "Map is now your default landing page"
            : "Overview is now your default landing page",
      });
    } catch (err) {
      toast({
        variant: "destructive",
        title: "Couldn't update preference",
        description: (err as Error).message,
      });
    } finally {
      setBusy(false);
    }
  };

  if (isDefault) {
    return (
      <span
        className={cn(
          "inline-flex items-center gap-1.5 rounded-md border border-primary/30 bg-primary/10 px-2.5 py-1 text-[11px] font-medium text-primary",
          className
        )}
        title="This is your sign-in landing page"
      >
        <Star className="h-3.5 w-3.5 fill-current" />
        Default
      </span>
    );
  }

  return (
    <Button
      variant="outline"
      size="sm"
      onClick={onClick}
      disabled={busy}
      className={className}
      title="Sign in here by default"
    >
      {busy ? (
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
      ) : (
        <Star className="h-3.5 w-3.5" />
      )}
      Set as default
    </Button>
  );
}
