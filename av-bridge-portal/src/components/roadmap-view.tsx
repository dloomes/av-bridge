import type { Roadmap, RoadmapItem, RoadmapStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

// RoadmapView: the product roadmap as a swimlane timeline (one column per
// period, one row per theme) that stacks by period below 760px. Every string
// from the API renders as plain text (React escapes it); nothing here emits
// HTML or links.

const STATUS_LABEL: Record<RoadmapStatus, string> = {
  committed: "Committed",
  planned: "Planned",
  exploring: "Exploring",
};

const LEGEND: { status: RoadmapStatus; text: string }[] = [
  { status: "committed", text: "in a named monthly release" },
  { status: "planned", text: "designed; the quarter may move" },
  { status: "exploring", text: "shaping it with customers" },
];

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function formatAsOf(iso: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso);
  if (!m) return iso;
  const mon = MONTHS[Number(m[2]) - 1];
  if (!mon) return iso;
  return `${Number(m[3])} ${mon} ${m[1]}`;
}

function Dot({ status }: { status: RoadmapStatus }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "h-2.5 w-2.5 shrink-0 rounded-full",
        status === "committed" && "bg-primary",
        status === "planned" && "border-2 border-primary/60",
        status === "exploring" && "border-2 border-dashed border-muted-foreground"
      )}
    />
  );
}

function Card({ item, themeName }: { item: RoadmapItem; themeName?: string }) {
  return (
    <article
      className={cn(
        "grid min-w-0 gap-1 rounded-md border bg-card px-3 py-2.5",
        item.status === "committed" && "border-l-[3px] border-l-primary",
        item.status === "planned" && "border-l-[3px] border-l-primary/50",
        item.status === "exploring" && "border-l-[3px] border-dashed border-l-muted-foreground bg-transparent"
      )}
    >
      {themeName && (
        <span className="font-mono text-[10.5px] uppercase tracking-wider text-muted-foreground">{themeName}</span>
      )}
      <h4 className="text-sm font-semibold leading-snug">{item.title}</h4>
      <p className="text-[12.5px] leading-snug text-muted-foreground">{item.description}</p>
      <div className="flex justify-between gap-2 text-muted-foreground">
        <span className="inline-flex items-center gap-1.5 font-mono text-[11px] uppercase tracking-wide">
          <Dot status={item.status} />
          {STATUS_LABEL[item.status] ?? item.status}
        </span>
        {item.release_month && (
          <span className="font-mono text-[11px] font-medium text-primary">{item.release_month} release</span>
        )}
      </div>
    </article>
  );
}

export function RoadmapView({ roadmap, sharedWith }: { roadmap: Roadmap; sharedWith?: string }) {
  const { settings, periods, themes, items, shipped } = roadmap;
  const themeName = (id: string) => themes.find((t) => t.id === id)?.name;
  const gridStyle = { gridTemplateColumns: `170px repeat(${Math.max(periods.length, 1)}, minmax(0, 1fr))` };

  return (
    <div className="mx-auto grid max-w-[1180px] gap-10 px-4 py-8 md:px-6 md:py-10">
      <header className="grid gap-3.5">
        {sharedWith !== undefined && (
          <p className="rounded-md border bg-muted px-3 py-2 text-sm">
            Shared with {sharedWith} by Involve. Please don&apos;t forward this link.
          </p>
        )}
        <div className="font-mono text-xs uppercase tracking-widest text-muted-foreground">
          {settings.title} · as of {formatAsOf(settings.as_of)}
        </div>
        <h1 className="text-balance text-3xl font-bold tracking-tight text-primary md:text-4xl">
          {settings.headline}
        </h1>
        <p className="max-w-[62ch] text-base text-muted-foreground md:text-[17px]">{settings.lede}</p>
        <div className="mt-1.5 flex flex-wrap gap-x-5 gap-y-2.5 text-[13px] text-muted-foreground" aria-label="Legend">
          {LEGEND.map((l) => (
            <span key={l.status} className="inline-flex items-center gap-2">
              <Dot status={l.status} />
              <b className="font-semibold text-foreground">{STATUS_LABEL[l.status]}</b>
              {l.text}
            </span>
          ))}
        </div>
      </header>

      {/* Wide: swimlane grid */}
      <div className="hidden overflow-x-auto min-[760px]:block" role="region" aria-label="Roadmap timeline" tabIndex={0}>
        <div className="grid min-w-[900px]" style={gridStyle}>
          <div className="border-b-2" />
          {periods.map((p) => (
            <div
              key={p.id}
              className={cn("grid gap-0.5 border-b-2 px-3 pb-3.5", p.is_current && "border-b-primary")}
            >
              <h3 className={cn("text-base font-semibold", p.is_current && "text-primary")}>{p.name}</h3>
              <span className="font-mono text-xs text-muted-foreground">{p.months}</span>
            </div>
          ))}
          {themes.map((t) => (
            <ThemeRow key={t.id} theme={t} periods={periods} items={items} />
          ))}
        </div>
      </div>

      {/* Narrow: one list per period */}
      <div className="grid gap-7 min-[760px]:hidden">
        {periods.map((p) => (
          <section key={p.id} className="grid gap-2.5">
            <div className={cn("grid gap-0.5 border-b-2 pb-2", p.is_current && "border-b-primary")}>
              <h3 className={cn("text-base font-semibold", p.is_current && "text-primary")}>{p.name}</h3>
              <span className="font-mono text-xs text-muted-foreground">{p.months}</span>
            </div>
            {items
              .filter((i) => i.period === p.position)
              .map((i) => (
                <Card key={i.id} item={i} themeName={themeName(i.theme_id)} />
              ))}
          </section>
        ))}
      </div>

      {shipped.length > 0 && (
        <section>
          <h2 className="mb-3 text-lg font-semibold text-primary">Recently shipped</h2>
          <ul className="grid">
            {shipped.map((s) => (
              <li key={s.id} className="grid grid-cols-[76px_minmax(0,1fr)] gap-3 border-t py-2 text-sm">
                <span className="pt-0.5 font-mono text-xs text-muted-foreground">{s.when}</span>
                <span>{s.title}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <p className="max-w-[80ch] border-t pt-4 text-[12.5px] text-muted-foreground">{settings.disclaimer}</p>
    </div>
  );
}

function ThemeRow({
  theme,
  periods,
  items,
}: {
  theme: Roadmap["themes"][number];
  periods: Roadmap["periods"];
  items: RoadmapItem[];
}) {
  return (
    <>
      <div className="grid content-start gap-1 border-b py-4 pr-4">
        <h2 className="text-sm font-semibold">{theme.name}</h2>
        {theme.note && <p className="text-[12.5px] text-muted-foreground">{theme.note}</p>}
      </div>
      {periods.map((p) => (
        <div
          key={p.id}
          className={cn(
            "grid min-w-0 content-start gap-2.5 border-b border-l px-2 py-3.5",
            p.is_current && "bg-primary/5"
          )}
        >
          {items
            .filter((i) => i.theme_id === theme.id && i.period === p.position)
            .map((i) => (
              <Card key={i.id} item={i} />
            ))}
        </div>
      ))}
    </>
  );
}
