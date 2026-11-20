// Environment markers (tab prefix, stripe, sidebar chip) exist so Involve
// staff can tell UAT and local apart. Customers use production, so production
// shows no environment labelling at all.
export function isProductionEnv(): boolean {
  const raw = (process.env.NEXT_PUBLIC_AV_BRIDGE_ENV ?? "").toLowerCase();
  return raw === "prod" || raw === "production";
}
