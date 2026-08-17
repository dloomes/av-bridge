import type { Metadata } from "next";
import "./globals.css";
import { AppShell } from "@/components/app-shell";

// Prefix the browser tab title with the env label so an operator with
// UAT + prod tabs open can tell them apart at a glance. Local dev stays
// unprefixed to keep the tab clean.
function envTitlePrefix(): string {
  const raw = (process.env.NEXT_PUBLIC_AV_BRIDGE_ENV ?? "").toLowerCase();
  if (raw === "prod" || raw === "production") return "[PROD] ";
  if (raw === "uat" || raw === "staging") return "[UAT] ";
  return "";
}

export const metadata: Metadata = {
  title: `${envTitlePrefix()}AV Bridge`,
  description: "On-prem AV device gateway portal",
};

// Env stripe tone — mirrors the sidebar footer badge palette. Kept inline
// (not a shared helper) because two callers isn't enough abstraction pressure
// to justify a module. Reads process.env directly; NEXT_PUBLIC_ inlining
// makes this work in both server and client bundles.
function envStripeClass(): string | null {
  const raw = (process.env.NEXT_PUBLIC_AV_BRIDGE_ENV ?? "").toLowerCase();
  if (raw === "prod" || raw === "production") return "bg-red-500";
  if (raw === "uat" || raw === "staging") return "bg-amber-400";
  return null; // local/dev: no stripe — the chip in the sidebar footer is enough
}

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const stripe = envStripeClass();
  return (
    <html lang="en" suppressHydrationWarning>
      <body className="antialiased">
        {stripe && (
          <div
            className={`fixed inset-x-0 top-0 h-[2px] z-50 ${stripe}`}
            aria-hidden="true"
          />
        )}
        <div className="flex min-h-screen">
          <AppShell>{children}</AppShell>
        </div>
      </body>
    </html>
  );
}
