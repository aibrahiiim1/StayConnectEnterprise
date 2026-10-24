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
    <div className="mt-1 flex items-start gap-2 rounded-md px-2.5 py-1.5 text-2xs text-sidebar-muted">
      <ShieldCheck className="mt-0.5 size-3.5 shrink-0" aria-hidden />
      <Link
        href="/account/security"
        onClick={onNavigate}
        className="flex-1 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active"
      >
        Protect your account: turn on two-step sign-in.
      </Link>
      <button
        type="button"
        aria-label="Dismiss the two-step sign-in suggestion"
        className="shrink-0 rounded hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active"
        onClick={() => { writeDismissed(email); setShow(false); }}
      >
        <X className="size-3.5" aria-hidden />
      </button>
    </div>
  );
}
