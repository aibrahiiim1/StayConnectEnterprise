"use client";

// Draw the authenticator QR code locally so its secret never reaches another service.
// Recovery codes appear once after enrollment.

import { useEffect, useId, useRef, useState } from "react";
import QRCode from "qrcode";
import { KeyRound, Smartphone } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { api, ApiError } from "@/lib/api";

export type SecondStep = "code" | "enrol";

type Enrolment = { secret: string; otpauth_uri: string; account: string };
type CodeResult = { must_change_password?: boolean; recovery_codes?: string[]; recovery_codes_left?: number };

export function groupKey(secret: string): string {
  return secret.replace(/(.{4})/g, "$1 ").trim();
}

export function SigninSecondStep({ step, onDone, onRestart }: {
  step: SecondStep;
  onDone: () => void;
  onRestart: (why: string) => void;
}) {
  const [enrolment, setEnrolment] = useState<Enrolment | null>(null);
  const [qr, setQr] = useState<string>("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [recovery, setRecovery] = useState<string[] | null>(null);
  const [lowOnCodes, setLowOnCodes] = useState<number | null>(null);
  const hintId = useId();

  // Focus new views so screen readers announce recovery codes and warnings.
  const heading = useRef<HTMLHeadingElement | null>(null);
  const view = recovery ? "recovery" : lowOnCodes !== null ? "low" : "form";
  useEffect(() => {
    if (view !== "form") heading.current?.focus();
  }, [view]);

  useEffect(() => {
    if (step !== "enrol") return;
    let live = true;
    api.post<Enrolment>("/v1/auth/login/enrol", {})
      .then(async (e) => {
        if (!live) return;
        setEnrolment(e);
        try { setQr(await QRCode.toDataURL(e.otpauth_uri, { margin: 1, width: 208 })); } catch { setQr(""); }
      })
      .catch((e) => {
        if (!live) return;
        if (e instanceof ApiError && (e.code === "signin_expired" || e.code === "signin_restart")) onRestart(e.message);
        else setErr(e);
      });
    return () => { live = false; };
  }, [step, onRestart]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setBusy(true);
    try {
      const r = await api.post<CodeResult>("/v1/auth/login/code", { code: code.trim() });
      if (r.recovery_codes && r.recovery_codes.length > 0) {
        setRecovery(r.recovery_codes);
        return;
      }
      if (typeof r.recovery_codes_left === "number" && r.recovery_codes_left <= 3) {
        setLowOnCodes(r.recovery_codes_left);
        return;
      }
      onDone();
    } catch (e) {
      if (e instanceof ApiError && (e.code === "signin_expired" || e.code === "signin_restart")) {
        onRestart(e.message);
        return;
      }
      setErr(e);
      setCode("");
    } finally {
      setBusy(false);
    }
  }

  if (recovery) {
    return (
      <div className="space-y-4">
        <div className="space-y-1.5">
          <h2 ref={heading} tabIndex={-1} className="text-emphasis focus:outline-none">Save your recovery codes</h2>
          <p className="text-sm text-muted-foreground">
            If you lose your phone, each of these codes signs you in once instead of the app. They are shown only
            now. Print them or keep them somewhere safe that is not your phone.
          </p>
        </div>
        <ul aria-label="Recovery codes" className="grid grid-cols-2 gap-2 rounded-lg border border-border bg-card p-3 font-mono text-sm">
          {recovery.map((c) => <li key={c}>{c}</li>)}
        </ul>
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="secondary" onClick={() => {
            const blob = new Blob([recovery.join("\n") + "\n"], { type: "text/plain" });
            const a = document.createElement("a");
            a.href = URL.createObjectURL(blob);
            a.download = "central-recovery-codes.txt";
            a.click();
            URL.revokeObjectURL(a.href);
          }}>Download</Button>
          <Button type="button" variant="primary" onClick={onDone}>I have saved them</Button>
        </div>
      </div>
    );
  }

  if (lowOnCodes !== null) {
    return (
      <div className="space-y-4">
        <h2 ref={heading} tabIndex={-1} className="text-emphasis focus:outline-none">Recovery codes running low</h2>
        <p className="text-sm">
          You signed in with a recovery code. {lowOnCodes === 0 ? "You have none left." : `You have ${lowOnCodes} left.`}{" "}
          Make a new set under Two-step sign-in in the sidebar.
        </p>
        <Button type="button" variant="primary" onClick={onDone}>Continue</Button>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      {/* The alert already announces itself; an outer live wrapper would repeat it. */}
      <ErrorBanner err={err} className="mb-0" />
      {step === "enrol" ? (
        <div className="space-y-3">
          <div className="space-y-1.5">
            <h2 className="flex items-center gap-2 text-emphasis"><Smartphone className="size-4" aria-hidden />Set up two-step sign-in</h2>
            <p className="text-sm text-muted-foreground">
              Open an authenticator app on your phone (Google Authenticator, Microsoft Authenticator or any other),
              add an account and scan this code. You do this once.
            </p>
          </div>
          {enrolment ? (
            <div className="flex flex-col items-center gap-2 rounded-lg border border-border bg-card p-3">
              {/* The browser-generated data URL needs no Next.js image optimization. */}
              {/* eslint-disable-next-line @next/next/no-img-element */}
              {qr && <img src={qr} alt="QR code to scan with your authenticator app" width={208} height={208} className="rounded bg-white p-1" />}
              <div className="text-center text-caption text-muted-foreground">
                Cannot scan? Enter this key in the app:
                <div className="mt-1 select-all break-all font-mono text-sm text-foreground">{groupKey(enrolment.secret)}</div>
              </div>
            </div>
          ) : (
            !err && <p className="text-sm text-muted-foreground">Preparing…</p>
          )}
        </div>
      ) : (
        <div className="space-y-1.5">
          <h2 className="flex items-center gap-2 text-emphasis"><KeyRound className="size-4" aria-hidden />Enter your code</h2>
          <p className="text-sm text-muted-foreground">
            The 6-digit code your authenticator app shows now. Lost your phone? Enter one of your recovery codes.
          </p>
        </div>
      )}
      <Field label={step === "enrol" ? "Code from the app" : "Code"}>
        <Input
          // Focus the new code field after password entry.
          // eslint-disable-next-line jsx-a11y/no-autofocus
          autoFocus
          required
          inputMode={step === "enrol" ? "numeric" : "text"}
          autoComplete="one-time-code"
          maxLength={32}
          value={code}
          onChange={(e) => setCode(e.target.value)}
        />
      </Field>
      {(() => {
        const why = busy ? null
          : step === "enrol" && !enrolment ? "Wait for the setup code to appear."
          : code.trim() === "" ? (step === "enrol" ? "Enter the code your app shows to continue." : "Enter your code to continue.")
          : null;
        return (
          <>
            <Button type="submit" disabled={busy || why !== null} aria-describedby={why ? hintId : undefined} className="w-full" size="lg">
              {busy ? "Checking…" : step === "enrol" ? "Confirm and sign in" : "Sign in"}
            </Button>
            {why && <p id={hintId} className="text-center text-caption text-muted-foreground">{why}</p>}
          </>
        );
      })()}
      <Button type="button" variant="ghost" className="w-full" onClick={() => onRestart("")}>Back</Button>
    </form>
  );
}
