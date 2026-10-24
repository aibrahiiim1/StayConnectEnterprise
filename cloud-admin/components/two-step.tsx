"use client";

// Turning two-step sign-in on and off under Two-step sign-in (D68). The QR code is drawn locally so the secret never
// reaches another service; recovery codes are shown once. Both start with the password step-up (/v1/auth/reauth).

import { useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { Button } from "@/components/ui/button";
import { Field, Input, useFieldErrors } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { api, ApiError, reauth, withStepUp } from "@/lib/api";

/** Fired on window when this operator's two-step sign-in changes, so the sidebar hint can re-check. */
export const MFA_CHANGED_EVENT = "onegate:mfa-changed";

export function groupKey(secret: string): string {
  return secret.replace(/(.{4})/g, "$1 ").trim();
}

function announceChange() {
  try { window.dispatchEvent(new Event(MFA_CHANGED_EVENT)); } catch { /* no window in some tests */ }
}

function passwordProblem(e: unknown): Error | unknown {
  return e instanceof ApiError && (e.status === 401 || e.code === "reauth_required")
    ? new Error("Your password was not accepted.") : e;
}

export function RecoveryCodes({ codes, label = "Recovery codes" }: { codes: string[]; label?: string }) {
  const [copied, setCopied] = useState(false);
  const text = codes.join("\n") + "\n";
  return (
    <div className="space-y-3">
      <ul aria-label={label} className="grid grid-cols-2 gap-2 rounded-lg border border-border bg-card p-3 font-mono text-sm">
        {codes.map((c) => <li key={c}>{c}</li>)}
      </ul>
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" variant="secondary" onClick={async () => {
          try { await navigator.clipboard.writeText(text); setCopied(true); } catch { setCopied(false); }
        }}>{copied ? "Copied" : "Copy"}</Button>
        <Button type="button" size="sm" variant="secondary" onClick={() => {
          const blob = new Blob([text], { type: "text/plain" });
          const a = document.createElement("a");
          a.href = URL.createObjectURL(blob);
          a.download = "central-recovery-codes.txt";
          a.click();
          URL.revokeObjectURL(a.href);
        }}>Download</Button>
      </div>
    </div>
  );
}

type Setup = { secret: string; otpauth_uri: string };

/** Password, then the QR code and the first code, then the recovery codes once. */
export function TwoStepEnable({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const [password, setPassword] = useState("");
  const [setup, setSetup] = useState<Setup | null>(null);
  const [qr, setQr] = useState("");
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const f = useFieldErrors<"code">();
  const heading = useRef<HTMLHeadingElement | null>(null);
  useEffect(() => { if (codes) heading.current?.focus(); }, [codes]);

  async function start(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      await reauth(password);
      setPassword("");
      const s = await api.post<Setup>("/v1/auth/mfa/setup", {});
      setSetup(s);
      try { setQr(await QRCode.toDataURL(s.otpauth_uri, { margin: 1, width: 208 })); } catch { setQr(""); }
    } catch (e) {
      setErr(passwordProblem(e));
    } finally { setBusy(false); }
  }

  async function confirmCode(e: React.FormEvent) {
    e.preventDefault();
    setErr(null); f.clear();
    const c = code.replace(/[\s-]/g, "");
    if (!/^\d{6}$/.test(c)) {
      f.fail({ code: "Enter the 6-digit code your authenticator app shows now." });
      return;
    }
    setBusy(true);
    try {
      const r = await withStepUp(() => api.post<{ recovery_codes: string[] }>("/v1/auth/mfa/confirm", { code: c }));
      setCodes(r.recovery_codes);
      announceChange();
    } catch (e) {
      if (e instanceof ApiError && (e.code === "wrong_code" || e.code === "code_required")) {
        setCode(""); f.fail({ code: e.message });
      } else setErr(e);
    } finally { setBusy(false); }
  }

  if (codes) {
    return (
      <div className="space-y-3">
        <div className="space-y-1">
          <h3 ref={heading} tabIndex={-1} className="text-emphasis focus:outline-none">Two-step sign-in is on. Save your recovery codes</h3>
          <p className="text-sm text-muted-foreground">
            If you lose your phone, each of these codes signs you in once instead of the app. They are shown only
            now. Print them or keep them somewhere safe that is not your phone.
          </p>
        </div>
        <RecoveryCodes codes={codes} />
        <Button type="button" size="sm" variant="primary" onClick={onDone}>I have saved them</Button>
      </div>
    );
  }

  if (setup) {
    return (
      <form onSubmit={confirmCode} className="space-y-3" noValidate>
        <ErrorBanner err={err} className="mb-0" />
        <p className="text-sm text-muted-foreground">
          Open an authenticator app on your phone (Google Authenticator, Microsoft Authenticator or any other), add an
          account and scan this code. Then enter the 6-digit code it shows.
        </p>
        <div className="flex flex-col items-center gap-2 rounded-lg border border-border bg-card p-3 sm:flex-row sm:items-start">
          {/* The browser-generated data URL needs no Next.js image optimization. */}
          {/* eslint-disable-next-line @next/next/no-img-element */}
          {qr && <img src={qr} alt="QR code to scan with your authenticator app" width={208} height={208} className="rounded bg-white p-1" />}
          <div className="text-caption text-muted-foreground">
            Cannot scan? Enter this key in the app:
            <div className="mt-1 select-all break-all font-mono text-sm text-foreground">{groupKey(setup.secret)}</div>
          </div>
        </div>
        <Field label="Code from the app" required error={f.error("code")}>
          <Input
            id={f.id("code")}
            // eslint-disable-next-line jsx-a11y/no-autofocus
            autoFocus
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={9}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            className="max-w-[12rem] font-mono tracking-widest"
          />
        </Field>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" size="sm" variant="primary" disabled={busy}>{busy ? "Checking…" : "Turn on"}</Button>
          <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={onCancel}>Cancel</Button>
        </div>
      </form>
    );
  }

  return (
    <form onSubmit={start} className="space-y-3">
      <ErrorBanner err={err} className="mb-0" />
      <Field label="Your password" required hint="Confirms it is you before an authenticator is added.">
        <Input
          // eslint-disable-next-line jsx-a11y/no-autofocus
          autoFocus
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </Field>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" variant="primary" disabled={busy || password === ""}>
          {busy ? "Checking…" : "Continue"}
        </Button>
        <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={onCancel}>Cancel</Button>
      </div>
    </form>
  );
}

/** Turning it off needs the password and a code from the second factor. */
export function TwoStepDisable({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const f = useFieldErrors<"code">();

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null); f.clear();
    setBusy(true);
    try {
      await reauth(password);
    } catch (e) {
      setErr(passwordProblem(e));
      setBusy(false);
      return;
    }
    try {
      await api.post("/v1/auth/mfa/disable", { code: code.trim() });
      announceChange();
      onDone();
    } catch (e) {
      if (e instanceof ApiError && (e.code === "wrong_code" || e.code === "code_required")) {
        setCode(""); f.fail({ code: e.message });
      } else setErr(e);
    } finally { setBusy(false); }
  }

  return (
    <form onSubmit={submit} className="space-y-3" noValidate>
      <ErrorBanner err={err} className="mb-0" />
      <p className="text-sm text-muted-foreground">
        After this, your password alone signs you in. Your authenticator app and recovery codes stop working.
      </p>
      <Field label="Your password" required>
        <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </Field>
      <Field label="Code" required hint="From your authenticator app, or a recovery code." error={f.error("code")}>
        <Input
          id={f.id("code")}
          autoComplete="one-time-code"
          maxLength={32}
          value={code}
          onChange={(e) => setCode(e.target.value)}
          className="max-w-[16rem] font-mono"
        />
      </Field>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" variant="danger" disabled={busy || password === "" || code.trim() === ""}>
          {busy ? "Turning off…" : "Turn off two-step sign-in"}
        </Button>
        <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={onCancel}>Cancel</Button>
      </div>
    </form>
  );
}
