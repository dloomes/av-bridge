"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft } from "lucide-react";
import { canGoBack } from "@/lib/nav-history";
import { cn } from "@/lib/utils";

// BackLink returns to the page the user came from, filters and all, and
// reads "Back". Opened directly (a bookmark, an emailed link, a new tab),
// there's nowhere to go back to: it links to `href` and reads `label`.
export function BackLink({
  href,
  label,
  className,
  iconClassName = "h-3 w-3",
}: {
  href: string;
  label: string;
  className?: string;
  iconClassName?: string;
}) {
  const router = useRouter();
  // Decided after mount: sessionStorage isn't there on the server.
  const [back, setBack] = useState(false);
  useEffect(() => setBack(canGoBack()), []);

  return (
    <Link
      href={href}
      onClick={(e) => {
        if (!back || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
        e.preventDefault();
        router.back();
      }}
      className={cn(
        "inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-sm",
        className
      )}
    >
      <ArrowLeft aria-hidden="true" className={iconClassName} />
      {back ? "Back" : label}
    </Link>
  );
}
