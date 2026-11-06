"use client";

import { useEffect, useRef, useState } from "react";
import Image from "next/image";
import QRCode from "qrcode";
import { Download, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm } from "@/components/ui/dialog";
import { Stepper } from "@/components/ui/data";
import { CopyButton } from "@/components/ui/patterns";
import { api, ApiError } from "@/lib/api";
import { FADE } from "@/app/(app)/backups/load-failure";

export const MFA_CHANGED_EVENT = "onegate:mfa-changed";

const NOTHING_CHANGED = "Nothing was changed.";

export function groupKey(secret: string): string {
  return secret.replace(/(.{4})/g, "$1 ").trim();
}

function announceChange() {
  try { window.dispatchEvent(new Event(MFA_CHANGED_EVENT)); } catch { return; }
}

export function twoStepError(e: unknown, fallback: string): string {
  const o = (e ?? {}) as { status?: unknown; code?: unknown; message?: unknown };
  const code = typeof o.code === "string" ? o.code : "";
  const text = typeof o.message === "string" ? o.message.trim() : "";
  if (code === "reauth_required") return `Your password was not accepted. ${NOTHING_CHANGED}`;
  if (code === "bad_request" || code === "invalid") return `Fill in every field. ${NOTHING_CHANGED}`;
  if (/^[A-Z]/.test(text) && /[.!?]$/.test(text)) return text;
  if (o.status === 429) return `Too many attempts. Wait a minute, then try again. ${NOTHING_CHANGED}`;
  return fallback;
}

export const isPasswordRefused = (e: unknown) => e instanceof ApiError && e.code === "reauth_required";

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
          document.body.appendChild(a);
          a.click();
          a.remove();
          URL.revokeObjectURL(a.href);
        }}><Download /> Download</Button>
      </div>
    </div>
  );
}

type Setup = { secret: string; otpauth_uri: string };

const STEPS = ["Confirm your password", "Add to your app", "Save recovery codes"];

export function TwoStepEnable({ onDone, onCancel, onEnabled }: {
  onDone: () => void;
  onCancel: () => void;
  onEnabled?: () => void;
}) {
  const [password, setPassword] = useState("");
  const [setup, setSetup] = useState<Setup | null>(null);
  const [qr, setQr] = useState("");
  const [code, setCode] = useState("");
  const [codeErr, setCodeErr] = useState<string | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [expired, setExpired] = useState(false);
  const heading = useRef<HTMLHeadingElement | null>(null);
  const passwordRef = useRef<HTMLInputElement | null>(null);
  const codeRef = useRef<HTMLInputElement | null>(null);

  const step = codes ? 2 : setup ? 1 : 0;
  useEffect(() => {
    if (step === 0) passwordRef.current?.focus();
    else if (step === 1) codeRef.current?.focus();
    else heading.current?.focus();
  }, [step]);

  function startAgain() {
    setSetup(null); setQr(""); setCode(""); setCodeErr(null); setErr(null); setExpired(false);
  }

  async function start(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      const s = await api.post<Setup>("/auth/mfa/setup", { password });
      setPassword("");
      setSetup(s);
      try { setQr(await QRCode.toDataURL(s.otpauth_uri, { margin: 1, width: 208 })); } catch { setQr(""); }
    } catch (e2) {
      setErr(twoStepError(e2, "Two-step sign-in could not be set up. Try again in a moment."));
      if (isPasswordRefused(e2)) { setPassword(""); passwordRef.current?.focus(); }
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
    } catch (e2) {
      if (isCodeError(e2)) { setCodeErr(e2.message); setCode(""); codeRef.current?.focus(); }
      else {
        setErr(twoStepError(e2, "Two-step sign-in could not be turned on. Try again in a moment."));
        if (e2 instanceof ApiError && e2.code === "nothing_to_confirm") setExpired(true);
      }
    } finally { setBusy(false); }
  }

  return (
    <div className="space-y-5">
      <Stepper steps={STEPS} current={step} />

      {codes ? (
        <div key="codes" className={`space-y-4 ${FADE}`}>
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
        <form key="scan" onSubmit={confirmCode} className={`space-y-4 ${FADE}`} noValidate>
          <ErrorBanner err={err} className="mb-0" />
          {expired && (
            <Button type="button" size="sm" variant="secondary" onClick={startAgain}><RotateCcw /> Start again</Button>
          )}
          <div className="grid gap-5 sm:grid-cols-[auto_minmax(0,1fr)]">
            <div className="flex justify-center sm:block">
              {qr && (
                <Image
                  unoptimized
                  src={qr}
                  alt="QR code to scan with your authenticator app"
                  width={176}
                  height={176}
                  className="rounded-md border border-border bg-white p-1.5"
                />
              )}
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
                  ref={codeRef}
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
            <Button type="submit" variant="primary" loading={busy} disabled={expired || !/^\d{6}$/.test(code.replace(/\s/g, ""))}>
              Turn on
            </Button>
          </div>
        </form>
      ) : (
        <form key="password" onSubmit={start} className={`space-y-4 ${FADE}`}>
          <ErrorBanner err={err} className="mb-0" />
          <Field label="Your password" required hint="Confirms it is you before an authenticator is added." className="max-w-sm">
            <Input
              ref={passwordRef}
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

export function TwoStepDisable({ open, onOpenChange, onDone }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [codeErr, setCodeErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const passwordRef = useRef<HTMLInputElement | null>(null);

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
      if (isCodeError(e)) { setCodeErr(e.message); setCode(""); }
      else {
        setErr(twoStepError(e, "Two-step sign-in could not be turned off. Try again in a moment. It is still on."));
        if (isPasswordRefused(e)) { setPassword(""); passwordRef.current?.focus(); }
      }
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
        <Input ref={passwordRef} type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
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
