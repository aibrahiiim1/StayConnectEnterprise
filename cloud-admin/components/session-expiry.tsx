"use client";

// Activity can extend the idle deadline, never the absolute deadline; recheck before warning and sign-out.
// Announce milestones rather than every countdown second to screen readers.

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { WARN_BEFORE_MS, nextEnding, type Deadlines, type Ending } from "@/lib/session-expiry";

/** Larger delays can fire immediately because setTimeout has a limit. */
const MAX_DELAY = 2 ** 31 - 1;

function clock(at: number): string {
  return new Date(at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function countdown(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

export function SessionExpiryWarning({
  deadlines,
  refresh,
  keepAlive,
  onExpired,
  onSignOut,
}: {
  deadlines: Deadlines;
  refresh: () => Promise<Deadlines>;
  keepAlive: () => Promise<unknown>;
  onExpired: () => void;
  onSignOut: () => void;
}) {
  // Equivalent re-reads must not restart timers.
  const next = nextEnding(deadlines);
  const endAt = next?.at ?? null;
  const endKind = next?.kind ?? null;
  const ending = React.useMemo<Ending | null>(
    () => (endAt === null || endKind === null ? null : { at: endAt, kind: endKind }),
    [endAt, endKind],
  );
  const [open, setOpen] = React.useState<Ending | null>(null);
  const [dismissedAt, setDismissedAt] = React.useState<number | null>(null);
  const [now, setNow] = React.useState(() => Date.now());
  const [busy, setBusy] = React.useState(false);
  const [spoken, setSpoken] = React.useState("");

  const refreshRef = React.useRef(refresh);
  refreshRef.current = refresh;
  const expiredRef = React.useRef(onExpired);
  expiredRef.current = onExpired;

  React.useEffect(() => {
    if (open && (!ending || ending.at - Date.now() > WARN_BEFORE_MS + 5000)) setOpen(null);
  }, [ending, open]);

  React.useEffect(() => {
    if (!ending || open || dismissedAt === ending.at) return;
    const delay = Math.min(MAX_DELAY, Math.max(0, ending.at - WARN_BEFORE_MS - Date.now()));
    let live = true;
    const t = setTimeout(async () => {
      let fresh: Ending | null = ending;
      try {
        fresh = nextEnding(await refreshRef.current());
      } catch {
        // Network failures keep the last known deadline; 401 handling occurs at the request boundary.
      }
      if (!live || !fresh) return;
      if (fresh.at - Date.now() > WARN_BEFORE_MS + 5000) return;
      setNow(Date.now());
      setSpoken("");
      setOpen(fresh);
    }, delay);
    return () => { live = false; clearTimeout(t); };
  }, [ending, open, dismissedAt]);

  React.useEffect(() => {
    if (!ending) return;
    const delay = Math.min(MAX_DELAY, Math.max(0, ending.at - Date.now()));
    let live = true;
    const t = setTimeout(async () => {
      try {
        const fresh = nextEnding(await refreshRef.current());
        if (live && fresh && fresh.at - Date.now() > 1000) return;
      } catch {
      }
      if (live) expiredRef.current();
    }, delay);
    return () => { live = false; clearTimeout(t); };
  }, [ending]);

  React.useEffect(() => {
    if (!open) return;
    const i = setInterval(() => {
      const n = Date.now();
      setNow(n);
      const left = open.at - n;
      setSpoken((prev) => {
        if (left <= 30_000 && !prev.startsWith("30")) return "30 seconds left.";
        if (left <= 60_000 && left > 30_000 && prev === "") return "1 minute left.";
        return prev;
      });
    }, 1000);
    return () => clearInterval(i);
  }, [open]);

  async function stay() {
    setBusy(true);
    try {
      await keepAlive();
      await refreshRef.current();
    } catch {
    } finally {
      setBusy(false);
    }
  }

  function acknowledge() {
    if (open) setDismissedAt(open.at);
    setOpen(null);
  }

  const absolute = open?.kind === "absolute";
  const left = open ? open.at - now : 0;

  return (
    <Dialog
      open={open !== null}
      onOpenChange={(v) => {
        if (v || busy) return;
        // Escape shows activity and may extend only the idle deadline.
        if (absolute) acknowledge();
        else void stay();
      }}
    >
      <DialogContent
        size="sm"
        role="alertdialog"
        onPointerDownOutside={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>{absolute ? "Your sign-in is about to end" : "Are you still there?"}</DialogTitle>
          <DialogDescription>
            {open && (absolute
              ? `For security, a sign-in lasts a fixed time however active you are. This one ends at ${clock(open.at)}; save your work, then sign in again.`
              : `You will be signed out at ${clock(open.at)} because there has been no activity. Choose Stay signed in to continue.`)}
          </DialogDescription>
        </DialogHeader>
        <DialogBody>
          <p className="text-center text-metric tabular" aria-hidden>{countdown(left)}</p>
          <p className="sr-only" role="status" aria-live="polite">{spoken}</p>
        </DialogBody>
        <DialogFooter>
          <Button type="button" variant="ghost" onClick={onSignOut} disabled={busy}>Sign out</Button>
          {absolute ? (
            <Button type="button" onClick={acknowledge}>OK</Button>
          ) : (
            <Button type="button" onClick={() => void stay()} disabled={busy}>
              {busy ? "Staying signed in…" : "Stay signed in"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
