import type { PrivacyDownload } from "@/lib/api";

// ApiError messages look like `404 Not Found: {"error":"..."}`. Show just the
// sentence when there is one.
export function plainError(e: unknown): string {
  const msg = (e as Error)?.message ?? "Something went wrong";
  const i = msg.indexOf("{");
  if (i >= 0) {
    try {
      const j = JSON.parse(msg.slice(i));
      if (typeof j?.error === "string") return j.error;
    } catch {}
  }
  return msg;
}

// saveDownload hands a fetched file to the browser as a download.
export function saveDownload({ blob, filename }: PrivacyDownload): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
