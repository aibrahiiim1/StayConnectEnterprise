"use client";

// Turning two-step sign-in on and off from the account page (D68). The QR code is drawn locally so the secret never
// reaches another service; recovery codes are shown once.

import { useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { Download } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm } from "@/components/ui/dialog";
import { Stepper } from "@/components/ui/data";
import { CopyButton } from "@/components/ui/patterns";
import { api, ApiError } from "@/lib/api";

/** Fired on window when this operator's two-step sign-in changes, so the sidebar hint can re-check. */
export const MFA_CHANGED_EVENT = "onegate:mfa-changed";

export function groupKey(secret: string): string {
  return secret.replace(/(.{4})/g, "$1 ").trim();
}

function announceChange() {
  try { window.dispatchEvent(new Event(MFA_CHANGED_EVENT)); } catch { /* no window in some tests */ }
}

function passwordProblem(e: unknown): unknown {
  return e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e;
}

const isCodeError = (e: unknown): e is ApiError =>
  e instanceof ApiError && (e.code === "wrong_code" || e.code === "code_required");

export function RecoveryCodes({ codes, label = "Recovery codes" }: { codes: string[]; label?: string }) {
  const text = codes.join("\n") + "\n";
  return (
    <div className="space-y-3">
      <ul
        aria-label={label}
        className="grid grid-cols-1 gap-x-6 gap-y-1.5 rounded-lg border border-border bg-surface px-4 py-3 font-mono text-sm tabular min-[400px]:grid-cols-2"
      >
        {codes.map((c) => <li key={c} className="select-all">{c}</li>)}
      </ul>
      <div className="flex flex-wrap gap-2">
        <CopyButton value={text} />
        <Button type="button" size="sm" variant="secondary" onClick={() => {
          const blob = new Blob([text], { type: "text/plain" });
          const a = document.createElement("a");
          a.href = URL.createObjectURL(blob);
          a.download = "admin-console-recovery-codes.txt";
          a.click();
          URL.revokeObjectURL(a.href);
        }}><Download /> Download</Button>
      </div>
    </div>
  );
}

type Setup = { secret: string; otpauth_uri: string };

const STEPS = ["Confirm your password", "Add to your app", "Save recovery codes"];

/** Password, then the QR code and the first code, then the recovery codes once. */
export function TwoStepEnable({ onDone, onCancel, onEnabled }: {
  onDone: () => void;
  onCancel: () => void;
  /** Called as soon as the first code is accepted, before the recovery codes are acknowledged. */
  onEnabled?: () => void;
}) {
  const [password, setPassword] = useState("");
  const [setup, setSetup] = useState<Setup | null>(null);
  const [qr, setQr] = useState("");
  const [code, setCode] = useState("");
  const [codeErr, setCodeErr] = useState<string | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const heading = useRef<HTMLHeadingElement | null>(null);
  useEffect(() => { if (codes) heading.current?.focus(); }, [codes]);

  async function start(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      const s = await api.post<Setup>("/auth/mfa/setup", { password });
      setPassword("");
      setSetup(s);
      try { setQr(await QRCode.toDataURL(s.otpauth_uri, { margin: 1, width: 208 })); } catch { setQr(""); }
    } catch (e) {
      setErr(passwordProblem(e));
    } finally { setBusy(false); }
  }

  async function confirmCode(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true); setErr(null); setCodeErr(null);
    try {
      const r = await api.post<{ recovery_codes: string[] }>("/auth/mfa/confirm", { code: code.replace(/\s/g, "") });
      setCodes(r.recovery_codes);
      announceChange();
      onEnabled?.();
    } catch (e) {
      if (isCodeError(e)) { setCodeErr(e.message); setCode(""); } else setErr(e);
    } finally { setBusy(false); }
  }

  const step = codes ? 2 : setup ? 1 : 0;
  return (
    <div className="space-y-5">
      <Stepper steps={STEPS} current={step} />

      {codes ? (
        <div className="space-y-4">
          <div className="space-y-1">
            <h3 ref={heading} tabIndex={-1} className="text-emphasis focus:outline-none">
              Two-step sign-in is on. Save your recovery codes
            </h3>
            <p className="text-sm text-muted-foreground">
              Each code signs you in once if you lose your phone. They are shown only now: keep them somewhere safe
              that is not your phone.
            </p>
          </div>
          <RecoveryCodes codes={codes} />
          <div className="flex justify-end border-t border-border pt-4">
            <Button type="button" variant="primary" onClick={onDone}>I have saved them</Button>
          </div>
        </div>
      ) : setup ? (
        <form onSubmit={confirmCode} className="space-y-4" noValidate>
          <ErrorBanner err={err} className="mb-0" />
          <div className="grid gap-5 sm:grid-cols-[auto_minmax(0,1fr)]">
            <div className="flex justify-center sm:block">
              {/* A browser-generated data URL needs no Next.js image optimization. */}
              {/* eslint-disable-next-line @next/next/no-img-element */}
              {qr && <img src={qr} alt="QR code to scan with your authenticator app" width={176} height={176} className="rounded-md border border-border bg-white p-1.5" />}
            </div>
            <div className="min-w-0 space-y-4">
              <p className="text-sm text-muted-foreground">
                In an authenticator app on your phone, add an account and scan this code. Then enter the 6-digit code
                it shows.
              </p>
              <div className="space-y-1">
                <div className="text-xs text-muted-foreground">Cannot scan? Enter this key instead</div>
                <div className="select-all break-all font-mono text-sm text-foreground">{groupKey(setup.secret)}</div>
              </div>
              <Field label="Code from the app" required error={codeErr ?? undefined}>
                <Input
                  // eslint-disable-next-line jsx-a11y/no-autofocus
                  autoFocus
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  maxLength={7}
                  value={code}
                  onChange={(e) => { setCode(e.target.value); setCodeErr(null); }}
                  className="max-w-[10rem] font-mono tracking-widest"
                />
              </Field>
            </div>
          </div>
          <div className="flex flex-wrap justify-end gap-2 border-t border-border pt-4">
            <Button type="button" variant="ghost" disabled={busy} onClick={onCancel}>Cancel</Button>
            <Button type="submit" variant="primary" loading={busy} disabled={!/^\d{6}$/.test(code.replace(/\s/g, ""))}>
              Turn on
            </Button>
          </div>
        </form>
      ) : (
        <form onSubmit={start} className="space-y-4">
          <ErrorBanner err={err} className="mb-0" />
          <Field label="Your password" required hint="Confirms it is you before an authenticator is added." className="max-w-sm">
            <Input
              // eslint-disable-next-line jsx-a11y/no-autofocus
              autoFocus
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
          <div className="flex flex-wrap justify-end gap-2 border-t border-border pt-4">
            <Button type="button" variant="ghost" disabled={busy} onClick={onCancel}>Cancel</Button>
            <Button type="submit" variant="primary" loading={busy} disabled={password === ""}>Continue</Button>
          </div>
        </form>
      )}
    </div>
  );
}

/** Turning it off needs the password and a code from the second factor. */
export function TwoStepDisable({ open, onOpenChange, onDone }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [codeErr, setCodeErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  useEffect(() => {
    if (!open) { setPassword(""); setCode(""); setCodeErr(null); setErr(null); }
  }, [open]);

  async function submit() {
    setBusy(true); setErr(null); setCodeErr(null);
    try {
      await api.post("/auth/mfa/disable", { password, code: code.trim() });
      announceChange();
      onDone();
    } catch (e) {
      if (isCodeError(e)) { setCodeErr(e.message); setCode(""); } else setErr(passwordProblem(e));
    } finally { setBusy(false); }
  }

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      size="sm"
      title="Turn off two-step sign-in?"
      description="Your password alone will sign you in. Your authenticator app and recovery codes stop working, and you are signed out everywhere else."
      submitLabel="Turn off two-step sign-in"
      submitVariant="danger"
      busyLabel="Turning off…"
      busy={busy}
      error={err}
      disabled={password === "" || code.trim() === ""}
      disabledReason="Enter your password and a code to continue."
      onSubmit={submit}
    >
      <Field label="Your password" required>
        <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </Field>
      <Field label="Code" required hint="From your authenticator app, or a recovery code." error={codeErr ?? undefined}>
        <Input
          autoComplete="one-time-code"
          spellCheck={false}
          maxLength={32}
          value={code}
          onChange={(e) => { setCode(e.target.value); setCodeErr(null); }}
          className="font-mono"
        />
      </Field>
    </DialogForm>
  );
}
