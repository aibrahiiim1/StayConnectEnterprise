"use client";

// TOASTS — the confirmation that an action happened.
//
// Before this, a successful save said nothing: the dialog closed and the operator had to infer from the table
// whether the change took. On a slow appliance that inference is wrong often enough to cause a second click, and a
// second click on "Activate" is a second activation attempt.
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

const DISMISS_AFTER_MS = 4500;
// Never `hidden` when empty either: a region must already be in the accessibility tree when content arrives.
const REGION = "flex w-full flex-col items-center gap-2 sm:items-end";

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

  // THE TIMER PAUSES WHILE THE OPERATOR IS WITH THE TOAST. Hovering it or tabbing to its Dismiss button stops the
  // clock, and leaving restarts it. An error never times out at all: it is the one the operator most needs to
  // finish reading, so it stays until dismissed.
  const [paused, setPaused] = React.useState(false);

  // Two live regions, created once and never nested: toasts are rendered INTO them, so each announcement comes
  // from exactly one region (polite for confirmations, assertive for errors) rather than a live role inside
  // another live region.
  const polite = items.filter((t) => t.tone !== "err");
  const assertive = items.filter((t) => t.tone === "err");

  return (
    <Ctx.Provider value={api}>
      {children}
      <div
        className="pointer-events-none fixed inset-x-0 bottom-0 z-[60] flex flex-col items-center gap-2 p-4 sm:items-end sm:p-6"
        onMouseEnter={() => setPaused(true)}
        onMouseLeave={() => setPaused(false)}
        onFocus={() => setPaused(true)}
        onBlur={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setPaused(false);
        }}
      >
        {/* Bare aria-live rather than role="status"/"alert": an alert role that is always on the page, empty,
            reads to assistive tech (and to tests) as an error that is always present. Not `display: contents`
            either: Safari drops such an element from the accessibility tree, live region included. */}
        <div aria-live="polite" aria-relevant="additions" className={REGION}>
          {polite.map((t) => (
            <ToastCard key={t.id} item={t} paused={paused} onDismiss={dismiss} />
          ))}
        </div>
        <div aria-live="assertive" aria-relevant="additions" className={REGION}>
          {assertive.map((t) => (
            <ToastCard key={t.id} item={t} paused={paused} onDismiss={dismiss} />
          ))}
        </div>
      </div>
    </Ctx.Provider>
  );
}

function ToastCard({
  item: t,
  paused,
  onDismiss,
}: {
  item: ToastItem;
  paused: boolean;
  onDismiss: (id: number) => void;
}) {
  // Time already shown survives a pause, so hovering at 4 s does not buy a fresh 4.5 s.
  const remaining = React.useRef(DISMISS_AFTER_MS);
  React.useEffect(() => {
    if (t.tone === "err" || paused) return;
    const started = Date.now();
    const timer = window.setTimeout(() => onDismiss(t.id), remaining.current);
    return () => {
      window.clearTimeout(timer);
      remaining.current = Math.max(0, remaining.current - (Date.now() - started));
    };
  }, [paused, t.id, t.tone, onDismiss]);

  const Icon = ICON[t.tone];
  return (
    <div
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
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        )}
      >
        <X className="size-3.5" />
        <span className="sr-only">Dismiss</span>
      </button>
    </div>
  );
}
