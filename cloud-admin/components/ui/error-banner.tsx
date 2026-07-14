import { ApiError } from "@/lib/api";

// formatError accepts either a thrown ApiError, a free-form string, or null.
// Returns the user-visible message + optional trace id for support tickets.
export function ErrorBanner({ err }: { err: unknown }) {
  if (err == null || err === "") return null;
  let message = "";
  let traceId: string | undefined;
  if (err instanceof ApiError) {
    message = err.message;
    traceId = err.traceId;
  } else if (typeof err === "string") {
    message = err;
  } else if (err instanceof Error) {
    message = err.message;
  } else {
    message = String(err);
  }
  return (
    <div className="mb-4 rounded-md border border-[#6b2128] bg-[#3a1418] text-err px-3 py-2 text-sm flex items-start justify-between gap-4">
      <span>{message}</span>
      {traceId && (
        <span className="text-xs text-muted font-mono shrink-0" title="Server trace id — include when reporting issues">
          {traceId}
        </span>
      )}
    </div>
  );
}
