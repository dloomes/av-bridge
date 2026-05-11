# av-bridge-portal

Next.js 14 web portal for the av-bridge on-prem AV gateway.

## Quick start

```bash
npm install
npm run dev
```

Open http://localhost:3000.

The portal expects av-bridge to be reachable at `http://localhost:8080`. To
override (e.g. running av-bridge on another host), edit `.env.local`:

```
NEXT_PUBLIC_AV_BRIDGE_HTTP=http://localhost:8080
NEXT_PUBLIC_AV_BRIDGE_WS=ws://localhost:8080
```

## Pages

- `/` — Dashboard: fleet summary, device grid, live event feed (auto-refresh every 15s)
- `/devices/[id]` — Device detail: live telemetry (10s), command panel, per-device event history
- `/health` — Raw `/healthz`, `/api/v1/status`, and `/metrics` output

## Stack

- Next.js 14 App Router · TypeScript · Tailwind CSS
- shadcn/ui-style components (Button, Card, Badge, Separator, ScrollArea, Skeleton)
- lucide-react icons
- Native `fetch` + `WebSocket` (no extra data-fetching lib for the PoC)
