"use client";

// THE WARNING BEFORE AN IDLE SIGN-OUT (WCAG 2.2.1).
//
// edged ends an Admin Console session after the site's idle limit (30 minutes by default, a setting) and the
// console's own background whoami check deliberately does not count as activity. An operator reading a long
// report, or away from the desk, used to find out only by being thrown to the sign-in page with whatever they were
// typing lost. Now, two minutes before the end, a dialog says so and offers to stay: "Stay signed in" makes one
// request that counts as activity (GET /auth/keepalive), which moves the end forward.
//
// THE END COMES FROM THE APPLIANCE (whoami's expires_at: the idle limit or the absolute one, whichever is first),
// not from a timer of our own, so other screens' requests -- which do count -- push it back without this
// component knowing why. At the moment it says the session has ended it asks once more before believing it.
//
// THE COUNTDOWN IS VISUAL. A live region re-reading "1:59, 1:58, ..." every second makes a screen reader
// unusable; the dialog's description is read once when it opens, and a polite line speaks at one minute and at
// thirty seconds.

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

/** How long before the end the warning opens. */
export const IDLE_WARNING_MS = 2 * 60_000;

function clock(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

export function IdleWarning({
  expiresAt,
  onStay,
  onCheck,
  onSignOut,
  onExpired,
}: {
  /** When the session ends if nothing else happens (whoami's expires_at). */
  expiresAt: string | null | undefined;
  /** A request that counts as activity; resolves to the new end. */
  onStay: () => Promise<string | null | undefined>;
  /** Asks the appliance again (whoami); resolves to the current end, or null when the session is gone. */
  onCheck: () => Promise<string | null | undefined>;
  onSignOut: () => void;
  /** The session has ended: send the operator to sign in again. */
  onExpired: () => void;
}) {
  const [endsAt, setEndsAt] = React.useState<number | null>(null);
  const [left, setLeft] = React.useState<number | null>(null);
  const [said, setSaid] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const checking = React.useRef(false);
  const stayRef = React.useRef<HTMLButtonElement | null>(null);

  // The appliance's answer wins whenever it changes.
  React.useEffect(() => {
    const t = expiresAt ? Date.parse(expiresAt) : NaN;
    setEndsAt(Number.isFinite(t) ? t : null);
  }, [expiresAt]);

  const latest = React.useRef({ onCheck, onExpired });
  React.useEffect(() => {
    latest.current = { onCheck, onExpired };
  });

  React.useEffect(() => {
    if (endsAt === null) {
      setLeft(null);
      return;
    }
    const tick = () => {
      const remaining = endsAt - Date.now();
      if (remaining > 0) {
        // Outside the window this sets null over null, which React skips: no re-render every second.
        setLeft(remaining <= IDLE_WARNING_MS ? remaining : null);
        return;
      }
      setLeft(0);
      if (checking.current) return;
      checking.current = true;
      // Another screen's request may have moved the end since whoami last answered: ask once before signing out.
      latest.current
        .onCheck()
        .then((next) => {
          const t = next ? Date.parse(next) : NaN;
          if (Number.isFinite(t) && t > Date.now()) setEndsAt(t);
          else latest.current.onExpired();
        })
        .catch(() => latest.current.onExpired())
        .finally(() => {
          checking.current = false;
        });
    };
    tick();
    const iv = window.setInterval(tick, 1000);
    return () => window.clearInterval(iv);
  }, [endsAt]);

  const open = left !== null;
  const bucket = left === null ? null : left <= 30_000 ? 30 : left <= 60_000 ? 60 : 120;
  React.useEffect(() => {
    if (bucket === 30) setSaid("30 seconds left.");
    else if (bucket === 60) setSaid("1 minute left.");
    else setSaid("");
  }, [bucket]);

  async function stay() {
    setBusy(true);
    try {
      const next = await onStay();
      const t = next ? Date.parse(next) : NaN;
      if (Number.isFinite(t)) setEndsAt(t);
    } catch {
      // The session ended while the dialog was open.
      onExpired();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      // Escape and the close button mean "I am here": the same as staying signed in.
      onOpenChange={(v) => {
        if (!v && !busy) void stay();
      }}
    >
      <DialogContent
        role="alertdialog"
        size="sm"
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          stayRef.current?.focus();
        }}
        onPointerDownOutside={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>You are about to be signed out</DialogTitle>
          <DialogDescription>
            Nothing has happened in the console for a while, so this session will end in about two minutes. Unsaved
            changes would be lost.
          </DialogDescription>
        </DialogHeader>
        <div className="px-5 py-4">
          {/* Hidden from assistive tech: the description above was read when the dialog opened, and the status
              line below speaks at one minute and at thirty seconds. */}
          <p className="text-sm text-muted-foreground" aria-hidden="true" data-testid="idle-countdown">
            Signing out in <span className="tabular font-semibold text-foreground">{clock(left ?? 0)}</span>
          </p>
          <p role="status" aria-live="polite" className="sr-only">
            {said}
          </p>
        </div>
        <DialogFooter>
          <Button type="button" variant="ghost" onClick={onSignOut} disabled={busy}>
            Sign out
          </Button>
          <Button ref={stayRef} type="button" variant="primary" onClick={() => void stay()} disabled={busy}>
            {busy ? "Staying signed in…" : "Stay signed in"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
