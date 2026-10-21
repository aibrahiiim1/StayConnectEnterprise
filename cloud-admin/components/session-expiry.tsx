"use client";

// THE WARNING BEFORE A SIGN-IN ENDS (WCAG 2.2.1 Timing Adjustable).
//
// Central ends a sign-in after a period without activity, and a fixed time after it began however active the person
// is. Two minutes before the earlier of the two, this asks whether to stay signed in. Staying makes a request that
// counts as activity, which moves the idle limit; nothing can move the absolute limit, so when that is the one
// coming the dialog says so and offers only to acknowledge it or sign out now.
//
// Before warning and before ending, the deadlines are read again: work in another tab is activity too.
//
// The countdown is visible but not read out every second: the dialog's description gives the time it ends, and a
// polite live region speaks once at one minute and once at thirty seconds.

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { WARN_BEFORE_MS, nextEnding, type Deadlines, type Ending } from "@/lib/session-expiry";

/** setTimeout's limit; a longer delay would fire at once. */
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
  /** Reads the deadlines again (whoami). Rejects when the sign-in has already ended. */
  refresh: () => Promise<Deadlines>;
  /** A request that counts as activity. */
  keepAlive: () => Promise<unknown>;
  onExpired: () => void;
  onSignOut: () => void;
}) {
  // Keyed by value, not by object: a re-read that reports the same deadline must not restart the timers.
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

  // A deadline that moved well past the warning window (activity here or in another tab) closes the warning.
  React.useEffect(() => {
    if (open && (!ending || ending.at - Date.now() > WARN_BEFORE_MS + 5000)) setOpen(null);
  }, [ending, open]);

  // The warning, two minutes before the end, once the deadline is confirmed.
  React.useEffect(() => {
    if (!ending || open || dismissedAt === ending.at) return;
    const delay = Math.min(MAX_DELAY, Math.max(0, ending.at - WARN_BEFORE_MS - Date.now()));
    let live = true;
    const t = setTimeout(async () => {
      let fresh: Ending | null = ending;
      try {
        fresh = nextEnding(await refreshRef.current());
      } catch {
        // An ended sign-in is handled where the request failed; a network failure keeps the known deadline.
      }
      if (!live || !fresh) return;
      if (fresh.at - Date.now() > WARN_BEFORE_MS + 5000) return; // moved on; the new deadlines reschedule this
      setNow(Date.now());
      setSpoken("");
      setOpen(fresh);
    }, delay);
    return () => { live = false; clearTimeout(t); };
  }, [ending, open, dismissedAt]);

  // The end itself, whether or not the warning is showing: confirm, then sign out.
  React.useEffect(() => {
    if (!ending) return;
    const delay = Math.min(MAX_DELAY, Math.max(0, ending.at - Date.now()));
    let live = true;
    const t = setTimeout(async () => {
      try {
        const fresh = nextEnding(await refreshRef.current());
        if (live && fresh && fresh.at - Date.now() > 1000) return; // extended meanwhile
      } catch {
        // fall through: it has ended
      }
      if (live) expiredRef.current();
    }, delay);
    return () => { live = false; clearTimeout(t); };
  }, [ending]);

  // The visible countdown, and the two spoken reminders.
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
      // An ended sign-in is handled where the request failed.
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
        // Escape is an answer too: for the idle limit the person is evidently here, so it counts as staying.
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
