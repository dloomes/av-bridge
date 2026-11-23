// Turns a thrown API error into text for people. The API throws
// "<status> <statusText>: <body>" where the body is usually {"error":"..."}.
export function apiMessage(e: unknown): string {
  if (e instanceof TypeError) {
    return "Couldn't reach M.A.R.C.U.S. — check your connection and try again.";
  }
  const m = e instanceof Error ? e.message : String(e);
  const i = m.indexOf(": ");
  const body = (i >= 0 ? m.slice(i + 2) : m).trim();
  try {
    const j = JSON.parse(body) as { error?: unknown };
    if (j && typeof j.error === "string" && j.error.trim()) return j.error.trim();
  } catch {
    // not JSON: use the text as is
  }
  return body || "Something went wrong";
}
