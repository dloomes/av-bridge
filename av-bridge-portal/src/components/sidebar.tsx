"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  Activity as ActivityIcon,
  BarChart3,
  Bell,
  Boxes,
  Cpu,
  HeartPulse,
  History,
  LayoutDashboard,
  MapPin,
  Moon,
  Palette,
  Radio,
  ShieldCheck,
  Users,
  KeyRound,
} from "lucide-react";
import { LocationNav } from "@/components/location-nav";
import { useBranding } from "@/components/branding-provider";
import { usePolling } from "@/hooks/usePolling";
import { useSession } from "@/hooks/useSession";
import { api } from "@/lib/api";
import { isAdmin } from "@/lib/session";
import { cn } from "@/lib/utils";
import type { AlertsSummary } from "@/lib/types";

// NavItem groups per-section entries with an optional badge. Grouping keeps
// the sidebar scannable — one glance says "this is Monitor, that is Manage."
// The role_visibility field gates vendor-only sections (Helpdesk) without
// leaking the concept to non-vendor users.
interface NavItem {
  href: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  badgeKey?: "alerts_open";
  vendorOnly?: boolean;
  adminOnly?: boolean;
  matchExact?: boolean; // pathname must equal href (used only for "/")
}

interface NavSection {
  title: string;
  items: NavItem[];
  vendorOnly?: boolean;
}

const SECTIONS: NavSection[] = [
  {
    title: "Monitor",
    items: [
      { href: "/", label: "Overview", icon: LayoutDashboard, matchExact: true },
      { href: "/alerts", label: "Alerts", icon: Bell, badgeKey: "alerts_open" },
      { href: "/reports", label: "Reports", icon: BarChart3 },
      { href: "/firmware", label: "Firmware", icon: Cpu },
      { href: "/audit", label: "Activity", icon: History },
    ],
  },
  {
    title: "Manage",
    items: [
      { href: "/locations", label: "Locations", icon: MapPin },
      { href: "/assets", label: "Assets", icon: Boxes },
      { href: "/nightly/schedule", label: "Room Readiness", icon: Moon },
      { href: "/users", label: "Users", icon: Users },
      { href: "/roles", label: "Roles", icon: KeyRound },
      { href: "/notifications", label: "Notifications", icon: Bell },
      { href: "/settings/branding", label: "Branding", icon: Palette, adminOnly: true },
    ],
  },
  {
    title: "Vendor",
    vendorOnly: true,
    items: [{ href: "/helpdesk", label: "Helpdesk", icon: ShieldCheck }],
  },
  {
    title: "System",
    items: [{ href: "/health", label: "Health", icon: HeartPulse }],
  },
];

export function Sidebar() {
  const pathname = usePathname();
  const session = useSession();
  const { branding } = useBranding();
  const isVendor = !!session.user?.is_vendor;
  // Vendor is treated as admin-equivalent for UI gating — the backend
  // vendor-bypass makes every permission check pass anyway, so hiding
  // admin links from vendor would just hurt their workflow.
  const canManageBranding = isAdmin(session.user?.role) || isVendor;

  // Poll the alerts summary so the sidebar Alerts item can show a live
  // count. Polls every 15s (matches the alerts page cadence) — sidebar is
  // shared across every page so this is the one place the badge lives.
  const summary = usePolling<AlertsSummary>(
    (signal) => api.alertsSummary(signal),
    15_000
  );
  const badges: Record<string, number> = {
    alerts_open: summary.data?.open ?? 0,
  };
  const criticalOpen = summary.data?.critical_open ?? 0;

  return (
    <aside className="hidden md:flex md:w-64 lg:w-72 flex-col bg-sidebar text-sidebar-foreground border-r border-white/5">
      <div className="px-5 py-6 flex items-center gap-2.5">
        {branding.logo_data_url ? (
          <img
            src={branding.logo_data_url}
            alt=""
            className="h-8 w-8 rounded-md object-contain bg-white/5 p-0.5"
          />
        ) : (
          <div className="h-8 w-8 rounded-md bg-primary/20 ring-1 ring-primary/30 flex items-center justify-center">
            <Radio className="h-4 w-4 text-primary" />
          </div>
        )}
        <div>
          <div className="font-semibold text-sm leading-tight">
            {branding.display_name || "Medio Assist"}
          </div>
          <div className="text-xs text-sidebar-foreground/50 leading-tight">
            AV Monitoring
          </div>
        </div>
      </div>

      <nav className="flex-1 overflow-y-auto px-3 pb-2 scrollbar-thin">
        {SECTIONS.filter((s) => !s.vendorOnly || isVendor).map((section) => (
          <div key={section.title} className="pt-3 first:pt-1">
            <div className="px-3 pb-1 text-[10px] font-semibold uppercase tracking-wider text-sidebar-foreground/40">
              {section.title}
            </div>
            <div className="space-y-0.5">
              {section.items
                .filter((it) => !it.vendorOnly || isVendor)
                .filter((it) => !it.adminOnly || canManageBranding)
                .map((item) => {
                  const active = item.matchExact
                    ? pathname === item.href
                    : pathname.startsWith(item.href);
                  const Icon = item.icon;
                  const badge = item.badgeKey ? badges[item.badgeKey] : 0;
                  return (
                    <Link
                      key={item.href}
                      href={item.href}
                      className={cn(
                        "flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors",
                        active
                          ? "bg-white/10 text-white"
                          : "text-sidebar-foreground/70 hover:bg-white/5 hover:text-white"
                      )}
                    >
                      <Icon className="h-4 w-4" />
                      <span className="flex-1">{item.label}</span>
                      {badge > 0 && (
                        <span
                          className={cn(
                            "inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1.5 text-[10px] font-semibold",
                            item.badgeKey === "alerts_open" && criticalOpen > 0
                              ? "bg-red-500 text-white"
                              : "bg-white/15 text-white"
                          )}
                          aria-label={`${badge} ${item.label.toLowerCase()}`}
                        >
                          {badge}
                        </span>
                      )}
                    </Link>
                  );
                })}
            </div>
          </div>
        ))}

        <div className="pt-4">
          <div className="px-3 pb-1 text-[10px] font-semibold uppercase tracking-wider text-sidebar-foreground/40">
            Places
          </div>
          <div className="pr-1">
            <LocationNav />
          </div>
        </div>
      </nav>

      <div className="px-5 py-4 border-t border-white/5 text-xs text-sidebar-foreground/50">
        <div className="flex items-center gap-2">
          <ActivityIcon className="h-3.5 w-3.5" />
          <span>PoC build</span>
        </div>
      </div>
    </aside>
  );
}
