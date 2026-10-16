"use client";

// CHANGE MY PASSWORD — the operator's own password, with the current one (edged POST /auth/password).
//
// Two places use it. Forced: a session that signed in with a weak or administrator-chosen password can do
// nothing else until it sets one of its own (the layout shows only this form). Voluntary: from the account
// menu, at /account/password. Either way the current password is required -- a console left open must not let
// a passer-by take the account -- and every other place the operator was signed in is signed out.

import { useState } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";

export const MIN_OPERATOR_PASSWORD = 10;

export function ChangePasswordForm({ email, onChanged, submitLabel = "Change password" }: {
  email?: string;
  onChanged: (notice: string) => void;
  submitLabel?: string;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  const problem =
    next === "" ? null
    : next.length < MIN_OPERATOR_PASSWORD ? `${MIN_OPERATOR_PASSWORD - next.length} more characters needed.`
    : email && next.trim().toLowerCase() === email.trim().toLowerCase() ? "It must not be your account name."
    : next === current ? "It must be different from your current password."
    : null;
  const mismatch = again !== "" && again !== next;
  const ready = current !== "" && next !== "" && !problem && again === next && !busy;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!ready) return;
    setBusy(true); setErr(null);
    try {
      const r = await api.post<{ notice?: string }>("/auth/password", { current_password: current, new_password: next });
      setCurrent(""); setNext(""); setAgain("");
      onChanged(r?.notice ?? "Your password was changed.");
    } catch (e2) {
      setErr(e2);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4" noValidate>
      <div aria-live="assertive"><ErrorBanner err={err} className="mb-0" /></div>
      <Field label="Current password" required>
        <Input type="password" autoComplete="current-password" value={current}
          onChange={(e) => setCurrent(e.target.value)} required />
      </Field>
      <Field label="New password" required
        hint={`At least ${MIN_OPERATOR_PASSWORD} characters, and not your account name.`}
        error={problem ?? undefined}>
        <Input type="password" autoComplete="new-password" value={next}
          onChange={(e) => setNext(e.target.value)} required minLength={MIN_OPERATOR_PASSWORD} />
      </Field>
      <Field label="Type the new password again" required error={mismatch ? "These do not match." : undefined}>
        <Input type="password" autoComplete="new-password" value={again}
          onChange={(e) => setAgain(e.target.value)} required />
      </Field>
      <Button type="submit" disabled={!ready} className="w-full sm:w-auto">
        {busy ? "Changing…" : submitLabel}
      </Button>
    </form>
  );
}
