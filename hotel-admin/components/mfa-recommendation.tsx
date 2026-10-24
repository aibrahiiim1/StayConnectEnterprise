"use client";

// A quiet sidebar hint for an account without two-step sign-in (D68). Never blocks, never shown once it is on, and
// dismissible per operator in this browser.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { ShieldCheck, X } from "lucide-react";
import { api, inBackground } from "@/lib/api";
import { MFA_CHANGED_EVENT } from "@/components/two-step";

function dismissKey(email: string) {
  return `onegate.mfa-hint-dismissed.${email.trim().toLowerCase()}`;
}

export function readDismissed(email: string): boolean {
  try { return window.localStorage.getItem(dismissKey(email)) === "1"; } catch { return false; }
}

function writeDismissed(email: string) {
  try { window.localStorage.setItem(dismissKey(email), "1"); } catch { /* storage unavailable: the hint returns next load */ }
}

export function MfaRecommendation({ email, collapsed, onNavigate }: {
  email?: string;
  collapsed?: boolean;
  onNavigate?: () => void;
}) {
  const [show, setShow] = useState(false);

  const check = useCallback(() => {
    if (!email || readDismissed(email)) { setShow(false); return; }
    // Any failure hides the hint: it is a suggestion, never an error.
    Promise.resolve()
      .then(() => inBackground(() => api.get<{ enrolled: boolean }>("/auth/mfa")))
      .then((s) => setShow(s?.enrolled === false))
      .catch(() => setShow(false));
  }, [email]);

  useEffect(() => {
    check();
    window.addEventListener(MFA_CHANGED_EVENT, check);
    return () => window.removeEventListener(MFA_CHANGED_EVENT, check);
  }, [check]);

  if (!show || collapsed || !email) return null;
  return (
    <div className="mt-1 flex items-center gap-2 rounded-md border border-sidebar-border bg-sidebar-accent/40 py-1 pe-1 ps-2.5 text-xs text-sidebar-muted">
      <ShieldCheck className="size-3.5 shrink-0 text-sidebar-active" aria-hidden />
      <Link
        href="/account/security"
        onClick={onNavigate}
        className="min-w-0 flex-1 rounded-sm py-0.5 leading-snug transition-colors hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        Recommended: turn on two-step sign-in
      </Link>
      <button
        type="button"
        aria-label="Dismiss the two-step sign-in suggestion"
        className="flex size-6 shrink-0 items-center justify-center rounded-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring pointer-coarse:size-11"
        onClick={() => { writeDismissed(email); setShow(false); }}
      >
        <X className="size-3.5" aria-hidden />
      </button>
    </div>
  );
}
