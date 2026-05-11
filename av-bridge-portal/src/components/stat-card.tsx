import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import type { LucideIcon } from "lucide-react";

type Tone = "neutral" | "success" | "destructive" | "warning";

const toneStyles: Record<Tone, { ring: string; iconBg: string; icon: string; value: string }> = {
  neutral: {
    ring: "",
    iconBg: "bg-muted",
    icon: "text-foreground/60",
    value: "text-foreground",
  },
  success: {
    ring: "ring-1 ring-success/20",
    iconBg: "bg-success/10",
    icon: "[color:hsl(var(--success))]",
    value: "[color:hsl(var(--success))]",
  },
  destructive: {
    ring: "ring-1 ring-destructive/20",
    iconBg: "bg-destructive/10",
    icon: "[color:hsl(var(--destructive))]",
    value: "[color:hsl(var(--destructive))]",
  },
  warning: {
    ring: "ring-1 ring-warning/20",
    iconBg: "bg-warning/10",
    icon: "[color:hsl(var(--warning))]",
    value: "[color:hsl(var(--warning))]",
  },
};

interface Props {
  label: string;
  value: number | string;
  icon: LucideIcon;
  tone?: Tone;
  hint?: string;
}

export function StatCard({ label, value, icon: Icon, tone = "neutral", hint }: Props) {
  const t = toneStyles[tone];
  return (
    <Card className={cn(t.ring)}>
      <CardContent className="p-5 flex items-center gap-4">
        <div
          className={cn(
            "h-11 w-11 rounded-md flex items-center justify-center",
            t.iconBg
          )}
        >
          <Icon className={cn("h-5 w-5", t.icon)} />
        </div>
        <div className="min-w-0">
          <div className="text-xs uppercase tracking-wide text-muted-foreground">
            {label}
          </div>
          <div className={cn("text-2xl font-semibold leading-tight", t.value)}>
            {value}
          </div>
          {hint && (
            <div className="text-xs text-muted-foreground mt-0.5">{hint}</div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
