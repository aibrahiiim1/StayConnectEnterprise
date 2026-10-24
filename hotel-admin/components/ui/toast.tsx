"use client";

// Toasts only confirm or report an action; anything the operator must still see later stays on the page.

import * as React from "react";
import { CheckCircle2, AlertTriangle, XCircle, Info, X } from "lucide-react";
import { cn } from "@/lib/utils";

type Tone = "ok" | "warn" | "err" | "info";
type ToastItem = { id: number; tone: Tone; title: string; description?: string };

type ToastApi = {
  toast: (t: { tone?: Tone; title: string; description?: string }) => void;
  success: (title: string, description?: string) => void;
  error: (title: string, description?: string) => void;
};

const Ctx = React.createContext<ToastApi | null>(null);

const NOOP: ToastApi = { toast: () => {}, success: () => {}, error: () => {} };

/** Bare page tests may render without a provider. */
export function useToast(): ToastApi {
  return React.useContext(Ctx) ?? NOOP;
}

const ICON: Record<Tone, React.ComponentType<{ className?: string }>> = {
  ok: CheckCircle2,
  warn: AlertTriangle,
  err: XCircle,
  info: Info,
};

const TONE: Record<Tone, string> = {
  ok: "text-success",
  warn: "text-warning",
  err: "text-destructive",
  info: "text-info",
};

// Errors stay until dismissed so they cannot vanish before the operator reads them.
const DISMISS_AFTER_MS: Partial<Record<Tone, number>> = { ok: 5000, info: 5000, warn: 8000 };

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = React.useState<ToastItem[]>([]);
  const seq = React.useRef(0);

  const dismiss = React.useCallback((id: number) => setItems((xs) => xs.filter((x) => x.id !== id)), []);

  const api = React.useMemo<ToastApi>(() => {
    const toast: ToastApi["toast"] = ({ tone = "info", title, description }) => {
      const id = ++seq.current;
      setItems((xs) => [...xs.slice(-3), { id, tone, title, description }]);
    };
    return {
      toast,
      success: (title, description) => toast({ tone: "ok", title, description }),
      error: (title, description) => toast({ tone: "err", title, description }),
    };
  }, []);

  return (
    <Ctx.Provider value={api}>
      {children}
      {/* Each toast owns its live role; nesting live regions would announce twice. */}
      <div className="pointer-events-none fixed inset-x-0 bottom-0 z-[60] flex flex-col items-center gap-2 p-4 sm:items-end sm:p-6">
        {items.map((t) => (
          <ToastCard key={t.id} item={t} onDismiss={dismiss} />
        ))}
      </div>
    </Ctx.Provider>
  );
}

// The timer pauses while the pointer or keyboard focus is on the toast, and keeps the time already elapsed.
function ToastCard({ item: t, onDismiss }: { item: ToastItem; onDismiss: (id: number) => void }) {
  const [paused, setPaused] = React.useState(false);
  const remaining = React.useRef(DISMISS_AFTER_MS[t.tone]);

  React.useEffect(() => {
    if (remaining.current === undefined || paused) return;
    const started = Date.now();
    const timer = window.setTimeout(() => onDismiss(t.id), remaining.current);
    return () => {
      window.clearTimeout(timer);
      if (remaining.current !== undefined) remaining.current = Math.max(0, remaining.current - (Date.now() - started));
    };
  }, [paused, onDismiss, t.id]);

  const Icon = ICON[t.tone];
  return (
    <div
      role={t.tone === "err" ? "alert" : "status"}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setPaused(false);
      }}
      className={cn(
        "pointer-events-auto flex w-full max-w-sm items-start gap-3 rounded-lg border border-border bg-popover px-3.5 py-3 text-popover-foreground shadow-overlay",
        "animate-in fade-in-0 slide-in-from-bottom-2 motion-reduce:animate-none",
      )}
    >
      <Icon className={cn("mt-0.5 size-4 shrink-0", TONE[t.tone])} />
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="text-sm font-medium">{t.title}</div>
        {t.description && <div className="text-xs leading-relaxed text-muted-foreground">{t.description}</div>}
      </div>
      <button
        type="button"
        onClick={() => onDismiss(t.id)}
        className={cn(
          "-me-1.5 -mt-1 inline-flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground",
          "hover:bg-accent hover:text-foreground pointer-coarse:size-11",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        )}
      >
        <X className="size-3.5" />
        <span className="sr-only">Dismiss</span>
      </button>
    </div>
  );
}
