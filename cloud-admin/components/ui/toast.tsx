"use client";

// Keep durable information on the page; toasts only confirm or report actions.

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
  warn: "text-warning-subtle-foreground",
  err: "text-destructive",
  info: "text-info",
};

// Allow time to read confirmations; interaction pauses the timer.
const DISMISS_AFTER_MS = 8000;
// The live region must exist before content arrives.
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

  // Pause while the operator reads a toast; errors stay until dismissed.
  const [paused, setPaused] = React.useState(false);

  // Separate live regions announce errors assertively and confirmations politely.
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
        {/* Keep empty live regions mounted; Safari drops display:contents regions from the accessibility tree. */}
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
  // Pausing preserves elapsed time.
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
