"use client";

// Use the appliance's expiry, which other requests may extend, and recheck before signing out.
// Announce only key countdown milestones so screen readers are not interrupted every second.

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

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
  expiresAt: string | null | undefined;
  onStay: () => Promise<string | null | undefined>;
  onCheck: () => Promise<string | null | undefined>;
  onSignOut: () => void;
  onExpired: () => void;
}) {
  const [endsAt, setEndsAt] = React.useState<number | null>(null);
  const [left, setLeft] = React.useState<number | null>(null);
  const [said, setSaid] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const checking = React.useRef(false);
  const stayRef = React.useRef<HTMLButtonElement | null>(null);

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
        setLeft(remaining <= IDLE_WARNING_MS ? remaining : null);
        return;
      }
      setLeft(0);
      if (checking.current) return;
      checking.current = true;
      // Recheck because another request may have extended the session.
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
      onExpired();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      // Dismissal counts as an explicit request to stay signed in.
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
        <DialogBody>
          {/* Announce milestones through the status line instead of reading every countdown tick. */}
          <p className="text-sm text-muted-foreground" aria-hidden="true" data-testid="idle-countdown">
            Signing out in <span className="tabular font-semibold text-foreground">{clock(left ?? 0)}</span>
          </p>
          <p role="status" aria-live="polite" className="sr-only">
            {said}
          </p>
        </DialogBody>
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
