import { api, ApiError } from "@/lib/api";

export function diagnosticsFileName(at: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `onegate-diagnostics-${at.getUTCFullYear()}${p(at.getUTCMonth() + 1)}${p(at.getUTCDate())}-${p(at.getUTCHours())}${p(at.getUTCMinutes())}${p(at.getUTCSeconds())}Z.json`;
}

export function diagnosticsError(e: unknown): string {
  if (e instanceof ApiError && e.status === 403) return "Your role cannot download diagnostics. An IT manager or site admin can.";
  if (e instanceof ApiError && e.status === 429) return "Too many requests in a short time. Wait a minute, then try again.";
  return "The diagnostics file could not be made. Check the appliance is reachable, then try again.";
}

export async function downloadDiagnostics(at: Date = new Date()): Promise<string> {
  const bundle = await api.get<unknown>("/diagnostics/bundle");
  const name = diagnosticsFileName(at);
  const url = URL.createObjectURL(new Blob([JSON.stringify(bundle, null, 2)], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
  return name;
}
