"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  Activity,
  LayoutDashboard,
  HeartPulse,
  Radio,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { LocationNav } from "@/components/location-nav";

const nav = [
  { href: "/", label: "Dashboard", icon: LayoutDashboard },
  { href: "/health", label: "Health", icon: HeartPulse },
];

export function Sidebar() {
  const pathname = usePathname();

  return (
    <aside className="hidden md:flex md:w-64 lg:w-72 flex-col bg-sidebar text-sidebar-foreground border-r border-white/5">
      <div className="px-5 py-6 flex items-center gap-2.5">
        <div className="h-8 w-8 rounded-md bg-primary/20 ring-1 ring-primary/30 flex items-center justify-center">
          <Radio className="h-4 w-4 text-primary" />
        </div>
        <div>
          <div className="font-semibold text-sm leading-tight">AV Bridge</div>
          <div className="text-xs text-sidebar-foreground/50 leading-tight">
            On-prem gateway
          </div>
        </div>
      </div>

      <nav className="px-3 py-2 space-y-1">
        {nav.map((item) => {
          const active =
            item.href === "/"
              ? pathname === "/"
              : pathname.startsWith(item.href);
          const Icon = item.icon;
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
              {item.label}
            </Link>
          );
        })}
      </nav>

      <div className="px-3 py-3">
        <div className="px-2 pb-2 text-[10px] font-semibold uppercase tracking-wider text-sidebar-foreground/40">
          Locations
        </div>
        <div className="flex-1 overflow-y-auto pr-1 scrollbar-thin">
          <LocationNav />
        </div>
      </div>

      <div className="mt-auto px-5 py-4 border-t border-white/5 text-xs text-sidebar-foreground/50">
        <div className="flex items-center gap-2">
          <Activity className="h-3.5 w-3.5" />
          <span>PoC build</span>
        </div>
      </div>
    </aside>
  );
}
