"use client";

// TOASTS — the confirmation that an action happened.
//
// Before this, a successful save said nothing: the dialog closed and the operator had to infer from the table
// whether the change took. On a slow appliance that inference is wrong often enough to cause a second click, and a
// second click on "Issue 50 vouchers" is 100 vouchers.
//
// Deliberately small and dependency-free. A toast confirms or reports — it never carries the only copy of
// information the operator needs later (a revealed code, an error explaining what to fix). Those stay in the page.

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

/** Safe outside a provider (tests render pages bare): the calls simply do nothing. */
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
  warn: "text-warning-subtle-foreground",
  err: "text-destructive",
  info: "text-info",
};

// How long a confirmation stays up. Errors have no entry: they stay until the operator dismisses them, because
// they are the ones most needed to finish reading and a timer cannot know how fast anyone reads.
const DISMISS_AFTER_MS: Partial<Record<Tone, number>> = { ok: 4500, info: 4500, warn: 7000 };

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
      {/*
        The stack itself is NOT a live region. Each toast is its own -- role="status" for a confirmation,
        role="alert" for a failure -- and wrapping those in an aria-live container made a live region inside a
        live region, which some screen readers announce twice and others not at all.
      */}
      <div className="pointer-events-none fixed inset-x-0 bottom-0 z-[60] flex flex-col items-center gap-2 p-4 sm:items-end sm:p-6">
        {items.map((t) => (
          <ToastCard key={t.id} item={t} onDismiss={dismiss} />
        ))}
      </div>
    </Ctx.Provider>
  );
}

// A toast's timer PAUSES while the pointer or keyboard focus is on it, and restarts in full when they leave. An
// operator halfway through the description, or tabbing to Dismiss, should not have it vanish underneath them.
function ToastCard({ item: t, onDismiss }: { item: ToastItem; onDismiss: (id: number) => void }) {
  const [paused, setPaused] = React.useState(false);
  const after = DISMISS_AFTER_MS[t.tone];

  React.useEffect(() => {
    if (after === undefined || paused) return;
    const timer = window.setTimeout(() => onDismiss(t.id), after);
    return () => window.clearTimeout(timer);
  }, [after, paused, onDismiss, t.id]);

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
        "pointer-events-auto flex w-full max-w-sm items-start gap-3 rounded-lg border border-border bg-popover p-3.5 text-popover-foreground shadow-lg",
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
          "-me-1.5 -mt-1.5 inline-flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground",
          "hover:bg-surface hover:text-foreground pointer-coarse:size-11",
        )}
      >
        <X className="size-3.5" />
        <span className="sr-only">Dismiss</span>
      </button>
    </div>
  );
}
