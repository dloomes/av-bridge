"use client";

// Wallboard view: shared dashboards full screen, for a wall screen or TV.
//
//   /display#token=dsp_…   a display link: no one signed in; the token is
//                          kept in memory only. Rotates between the link's
//                          dashboards. A revoked link says so.
//   /display?id=<id>       the Display button: the signed-in user's own
//                          session, one dashboard.
//
// Dark theme and larger type for viewing across a room, the cursor hides
// when idle, the layout shifts by a few pixels every few minutes against
// screen burn, and a banner says when the information stops updating.

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Maximize, WifiOff } from "lucide-react";
import { DashboardGrid } from "@/components/dashboard/grid";
import { useSession } from "@/hooks/useSession";
import { api, setDisplayToken, type Dashboard } from "@/lib/api";
import { cn } from "@/lib/utils";

const CHECK_MS = 60_000; // how often the screen checks its dashboards
const STALE_MS = 3 * 60_000; // no successful check for this long → banner

type Mode = { kind: "link"; token: string } | { kind: "user"; id: string } | { kind: "none" };

export default function DisplayPage() {
  return (
    <Suspense fallback={null}>
      <Display />
    </Suspense>
  );
}

function readMode(params: URLSearchParams): Mode {
  if (typeof window !== "undefined") {
    const hash = new URLSearchParams(window.location.hash.replace(/^#/, ""));
    const token = hash.get("token");
    if (token) return { kind: "link", token };
  }
  const id = params.get("id");
  return id ? { kind: "user", id } : { kind: "none" };
}

function Display() {
  const params = useSearchParams();
  const session = useSession();
  const [mode, setMode] = useState<Mode | null>(null);
  const [ready, setReady] = useState(false);

  // Read the token or id once, on the client, before any widget fetches.
  useEffect(() => {
    const m = readMode(new URLSearchParams(params.toString()));
    if (m.kind === "link") setDisplayToken(m.token);
    setMode(m);
    setReady(true);
    return () => setDisplayToken(null);
  }, [params]);

  // Dark theme while on the wall.
  useEffect(() => {
    const root = document.documentElement;
    const hadDark = root.classList.contains("dark");
    root.classList.add("dark");
    root.style.fontSize = "112.5%";
    return () => {
      if (!hadDark) root.classList.remove("dark");
      root.style.fontSize = "";
    };
  }, []);

  if (!ready || !mode) return null;
  if (mode.kind === "none") {
    return <Message title="Nothing to display" body="Open a display link, or use Display on a dashboard." />;
  }
  if (mode.kind === "user" && session.hydrated && !session.token) {
    return (
      <Message title="Sign in to display this dashboard" body="Or ask an administrator for a display link, which needs no sign-in.">
        <Link href="/sign-in" className="text-sm underline">Sign in</Link>
      </Message>
    );
  }
  if (mode.kind === "user" && !session.hydrated) return null;
  return <Wall mode={mode} />;
}

function Wall({ mode }: { mode: Exclude<Mode, { kind: "none" }> }) {
  const [ids, setIds] = useState<string[]>(mode.kind === "user" ? [mode.id] : []);
  const [rotate, setRotate] = useState(60);
  const [name, setName] = useState("");
  const [index, setIndex] = useState(0);
  const [dashboard, setDashboard] = useState<Dashboard | null>(null);
  const [revoked, setRevoked] = useState(false);
  const [empty, setEmpty] = useState(false);
  const [lastOk, setLastOk] = useState(() => Date.now());
  const [now, setNow] = useState(() => Date.now());

  // The link's dashboards, re-checked so changes and revocation show up.
  const check = useCallback(async () => {
    try {
      if (mode.kind === "link") {
        const d = await api.currentDisplay();
        setName(d.name);
        setRotate(d.rotate_seconds);
        setIds((prev) => (prev.join() === d.dashboard_ids.join() ? prev : d.dashboard_ids));
        setEmpty(d.dashboard_ids.length === 0);
      }
      setLastOk(Date.now());
    } catch (e) {
      if (/^401\b/.test((e as Error).message)) setRevoked(true);
    }
  }, [mode.kind]);

  useEffect(() => {
    void check();
    const t = setInterval(check, CHECK_MS);
    return () => clearInterval(t);
  }, [check]);

  // Rotation.
  useEffect(() => {
    if (ids.length < 2) {
      setIndex(0);
      return;
    }
    const t = setInterval(() => setIndex((i) => (i + 1) % ids.length), rotate * 1000);
    return () => clearInterval(t);
  }, [ids, rotate]);

  // The dashboard on screen, re-read with each check so edits appear.
  const currentId = ids[index % Math.max(1, ids.length)];
  useEffect(() => {
    if (!currentId) return;
    let cancelled = false;
    const load = () =>
      api
        .getDashboard(currentId)
        .then((d) => {
          if (cancelled) return;
          setDashboard(d);
          setLastOk(Date.now());
        })
        .catch((e) => {
          if (/^401\b/.test((e as Error).message)) setRevoked(true);
        });
    void load();
    const t = setInterval(load, CHECK_MS);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [currentId]);

  // Clock for the header, and the out-of-date check.
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 15_000);
    const offline = () => setNow(Date.now());
    window.addEventListener("offline", offline);
    window.addEventListener("online", offline);
    return () => {
      clearInterval(t);
      window.removeEventListener("offline", offline);
      window.removeEventListener("online", offline);
    };
  }, []);
  const stale = now - lastOk > STALE_MS || (typeof navigator !== "undefined" && !navigator.onLine);

  const { idle, shift } = useWallBehaviour();

  if (revoked) {
    return (
      <Message
        title="This display link has been revoked"
        body="Ask an administrator for a new display link from Dashboards → Display links."
      />
    );
  }
  if (empty) {
    return <Message title={name || "Display"} body="This display link has no shared dashboards to show." />;
  }

  return (
    <div
      className={cn("min-h-screen bg-background text-foreground", idle && "cursor-none")}
      style={{ transform: `translate(${shift.x}px, ${shift.y}px)` }}
    >
      {stale && (
        <div role="alert" className="flex items-center justify-center gap-2 bg-destructive px-4 py-2 text-sm font-medium text-destructive-foreground">
          <WifiOff className="h-4 w-4" />
          Not updating since {new Date(lastOk).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })}.
          Check this screen&apos;s network connection.
        </div>
      )}
      <header className="flex items-center justify-between gap-4 px-6 pt-4">
        <div className="min-w-0">
          <h1 className="truncate text-2xl font-semibold">{dashboard?.name ?? name}</h1>
          {ids.length > 1 && (
            <div className="mt-1 flex gap-1.5" aria-label={`Dashboard ${index + 1} of ${ids.length}`}>
              {ids.map((id, i) => (
                <span key={id} className={cn("h-1.5 w-6 rounded-full", i === index ? "bg-foreground" : "bg-muted")} />
              ))}
            </div>
          )}
        </div>
        <div className="flex items-center gap-4">
          {!idle && (
            <button
              type="button"
              onClick={() => document.documentElement.requestFullscreen?.().catch(() => {})}
              className="flex items-center gap-1.5 rounded-md border px-3 py-1.5 text-sm hover:bg-accent/40"
            >
              <Maximize className="h-4 w-4" /> Full screen
            </button>
          )}
          <span className="text-2xl font-semibold tabular-nums">
            {new Date(now).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })}
          </span>
        </div>
      </header>
      <main className="p-6">
        {dashboard ? (
          dashboard.layout.widgets.length === 0 ? (
            <p className="text-muted-foreground">This dashboard has no widgets yet.</p>
          ) : (
            <DashboardGrid
              key={dashboard.id}
              widgets={dashboard.layout.widgets}
              editing={false}
              onChange={() => {}}
              onSettings={() => {}}
              tall
            />
          )
        ) : null}
      </main>
    </div>
  );
}

// Hide the cursor after a few idle seconds, and nudge the layout by a few
// pixels every five minutes so static parts don't burn into the screen.
function useWallBehaviour() {
  const [idle, setIdle] = useState(false);
  const [shift, setShift] = useState({ x: 0, y: 0 });
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    const wake = () => {
      setIdle(false);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setIdle(true), 3000);
    };
    wake();
    window.addEventListener("mousemove", wake);
    window.addEventListener("keydown", wake);
    const nudge = setInterval(() => {
      setShift({ x: Math.round(Math.random() * 6 - 3), y: Math.round(Math.random() * 6 - 3) });
    }, 5 * 60_000);
    return () => {
      window.removeEventListener("mousemove", wake);
      window.removeEventListener("keydown", wake);
      if (timer.current) clearTimeout(timer.current);
      clearInterval(nudge);
    };
  }, []);
  return { idle, shift };
}

function Message({ title, body, children }: { title: string; body: string; children?: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-6 text-center text-foreground">
      <div className="max-w-md space-y-3">
        <h1 className="text-2xl font-semibold">{title}</h1>
        <p className="text-muted-foreground">{body}</p>
        {children}
      </div>
    </div>
  );
}
