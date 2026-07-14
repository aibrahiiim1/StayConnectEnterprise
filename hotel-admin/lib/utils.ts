import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log10(n) / 3));
  const v = n / Math.pow(1000, i);
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : v >= 10 ? 1 : 2)} ${units[i]}`;
}

export function formatDate(s: string): string {
  if (!s) return "—";
  try {
    return new Date(s).toLocaleString();
  } catch {
    return s;
  }
}

// errMsg turns any thrown value into a string, appending the server trace id
// from ApiError when present. Use this when storing errors as plain strings;
// use <ErrorBanner err={...} /> when you can store the raw error.
import type { ApiError } from "./api";
export function errMsg(e: unknown): string {
  if (!e) return "";
  if (typeof e === "object" && e !== null && "traceId" in e && "message" in e) {
    const ae = e as ApiError;
    return ae.traceId ? `${ae.message} (trace ${ae.traceId})` : ae.message;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}

export function formatRelative(s?: string | null): string {
  if (!s) return "—";
  const then = new Date(s).getTime();
  const diff = Date.now() - then;
  if (diff < 0) return new Date(s).toLocaleString();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  return `${days}d ago`;
}
