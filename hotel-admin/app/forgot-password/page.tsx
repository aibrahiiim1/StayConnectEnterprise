"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { ArrowLeft, Eye, EyeOff } from "lucide-react";
import { BySemantics, OneGateLockup } from "@/components/brand";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { Callout } from "@/components/ui/error-banner";
import { ThemeToggle } from "@/components/theme-toggle";
import { api } from "@/lib/api";
import { resetConfirmError, resetRequestError, type ConfirmProblem } from "@/lib/password-reset";
import { cn } from "@/lib/utils";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const MIN_PASSWORD = 10;

type Step = "checking" | "unavailable" | "request" | "confirm" | "done";

function Secret({
  id, value, onChange, autoComplete, invalid, describedBy, inputRef,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  autoComplete: string;
  invalid?: boolean;
  describedBy?: string;
  inputRef?: React.RefObject<HTMLInputElement>;
}) {
  const [shown, setShown] = useState(false);
  return (
    <div className="relative">
      <Input
        ref={inputRef}
        id={id}
        type={shown ? "text" : "password"}
        autoComplete={autoComplete}
        autoCapitalize="none"
        spellCheck={false}
        value={value}
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
        onChange={(e) => onChange(e.target.value)}
        className="pe-10"
      />
      <button
        type="button"
        onClick={() => setShown((s) => !s)}
        aria-label={shown ? "Hide password" : "Show password"}
        aria-pressed={shown}
        className="absolute inset-y-0 end-0 flex w-10 items-center justify-center rounded-e-md text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&_svg]:size-4"
      >
        {shown ? <EyeOff aria-hidden /> : <Eye aria-hidden />}
      </button>
    </div>
  );
}

export default function ForgotPasswordPage() {
  const [step, setStep] = useState<Step>("checking");
  const [minutes, setMinutes] = useState<number | null>(null);
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [mfaCode, setMfaCode] = useState("");
  const [needsMfa, setNeedsMfa] = useState(false);
  const [busy, setBusy] = useState(false);
  const [requestErr, setRequestErr] = useState<string | null>(null);
  const [sentNote, setSentNote] = useState<string | null>(null);
  const [problem, setProblem] = useState<ConfirmProblem | null>(null);
  const [emailErr, setEmailErr] = useState<string | null>(null);
  const emailRef = useRef<HTMLInputElement>(null);
  const codeRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const mfaRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let live = true;
    api.get<{ available?: boolean; code_minutes?: number }>("/auth/password-reset")
      .then((r) => {
        if (!live) return;
        setMinutes(r?.code_minutes ?? null);
        setStep(r?.available ? "request" : "unavailable");
      })
      .catch(() => { if (live) setStep("unavailable"); });
    return () => { live = false; };
  }, []);

  useEffect(() => {
    if (step === "request") emailRef.current?.focus();
    if (step === "confirm") codeRef.current?.focus();
  }, [step]);

  useEffect(() => {
    if (!problem) return;
    const target = problem.field === "code" ? codeRef : problem.field === "password" ? passwordRef : problem.field === "mfa" ? mfaRef : null;
    requestAnimationFrame(() => target?.current?.focus());
  }, [problem]);

  async function requestCode(e?: React.FormEvent) {
    e?.preventDefault();
    setRequestErr(null);
    if (email.trim() === "") {
      setEmailErr("Enter the email address of your account.");
      emailRef.current?.focus();
      return;
    }
    setBusy(true);
    try {
      const r = await api.post<{ message?: string; code_minutes?: number }>("/auth/password-reset/request", { email: email.trim() });
      if (r?.code_minutes) setMinutes(r.code_minutes);
      setSentNote(r?.message ?? null);
      setCode(""); setMfaCode(""); setProblem(null);
      setStep("confirm");
    } catch (err) {
      setRequestErr(resetRequestError(err));
    } finally {
      setBusy(false);
    }
  }

  async function confirm(e: React.FormEvent) {
    e.preventDefault();
    setProblem(null);
    const digits = code.replace(/\s+/g, "");
    if (!/^\d{8}$/.test(digits)) { setProblem({ field: "code", message: "Enter the 8-digit code from the email." }); return; }
    if (password.length < MIN_PASSWORD) { setProblem({ field: "password", message: `Choose a password of at least ${MIN_PASSWORD} characters.` }); return; }
    if (password !== again) { setProblem({ field: "password", message: "The two passwords are not the same. Type the new password again in both boxes." }); return; }
    if (needsMfa && mfaCode.trim() === "") { setProblem({ field: "mfa", message: "Enter the 6-digit code your authenticator app shows now, or a recovery code." }); return; }
    setBusy(true);
    try {
      await api.post("/auth/password-reset/confirm", {
        email: email.trim(), code: digits, password, mfa_code: needsMfa ? mfaCode.trim() : "",
      });
      setPassword(""); setAgain(""); setCode(""); setMfaCode("");
      setStep("done");
    } catch (err) {
      const p = resetConfirmError(err);
      if (p.field === "mfa") setNeedsMfa(true);
      if (p.field === "mfa") setMfaCode("");
      setProblem(p);
    } finally {
      setBusy(false);
    }
  }

  const fieldError = (f: ConfirmProblem["field"]) => (problem?.field === f ? problem.message : undefined);

  return (
    <div className="flex min-h-screen flex-col bg-background">
      <header className="flex items-center justify-between gap-3 px-4 py-4 sm:px-6">
        <OneGateLockup product="Admin Console" />
        <ThemeToggle />
      </header>

      <main className="flex flex-1 items-start justify-center px-4 pb-12 pt-6 sm:items-center sm:px-6 sm:pt-0">
        <div className="w-full max-w-[24rem] space-y-4">
          <div className="rounded-xl border border-border bg-card p-6 text-card-foreground shadow-card sm:p-7">
            <div className="mb-6 space-y-1">
              <h1 className="text-title">{step === "done" ? "Password changed" : "Forgot your password?"}</h1>
              <p className="text-sm text-muted-foreground">
                {step === "request" && "We will email a code to the address of your account."}
                {step === "confirm" && "Enter the code from the email and choose a new password."}
                {step === "done" && "Sign in with your new password."}
                {(step === "unavailable" || step === "checking") && "For your account on this appliance."}
              </p>
            </div>

            {step === "checking" && <p className="text-sm text-muted-foreground" role="status">Checking whether this appliance can send a code…</p>}

            {step === "unavailable" && (
              <div className={cn("space-y-4", FADE)}>
                <Callout tone="neutral">
                  This appliance cannot email a reset code. Ask a site admin to reset your password under Operators.
                  If you are the only site admin, use the password reset command on the appliance described in the
                  installation manual.
                </Callout>
                <Link href="/login" className={cn(buttonVariants({ variant: "secondary" }), "w-full")}>Back to sign in</Link>
              </div>
            )}

            {step === "request" && (
              <form onSubmit={requestCode} className={cn("space-y-4", FADE)} noValidate>
                <Field label="Email address" error={emailErr ?? undefined}>
                  <Input
                    ref={emailRef}
                    type="email"
                    autoComplete="username"
                    autoCapitalize="none"
                    spellCheck={false}
                    value={email}
                    onChange={(e) => { setEmail(e.target.value); setEmailErr(null); }}
                  />
                </Field>
                {requestErr && <div className={FADE}><Callout tone="danger">{requestErr}</Callout></div>}
                <Button type="submit" loading={busy} className="w-full" size="lg">Email me a code</Button>
              </form>
            )}

            {step === "confirm" && (
              <form onSubmit={confirm} className={cn("space-y-4", FADE)} noValidate>
                <Callout tone="info">
                  {sentNote ?? "If that email address belongs to an account here that can reset its password, a code is on its way."}
                  {" "}Check your spam folder if it does not arrive within a few minutes.
                </Callout>
                <Field label="Code from the email" error={fieldError("code")} hint={minutes ? `8 digits. It works for ${minutes} minutes.` : "8 digits."}>
                  <Input
                    ref={codeRef}
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    maxLength={12}
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    className="tabular tracking-widest"
                  />
                </Field>
                <Field label="New password" error={fieldError("password")} hint={`At least ${MIN_PASSWORD} characters.`}>
                  <Secret id="new-password" inputRef={passwordRef} value={password} onChange={setPassword} autoComplete="new-password" invalid={problem?.field === "password"} />
                </Field>
                <Field label="New password again">
                  <Secret id="new-password-again" value={again} onChange={setAgain} autoComplete="new-password" invalid={problem?.field === "password"} />
                </Field>
                {needsMfa && (
                  <div className={FADE}>
                    <Field label="Authenticator code" error={fieldError("mfa")} hint="The 6-digit code your authenticator app shows now, or a recovery code.">
                      <Input
                        ref={mfaRef}
                        inputMode="numeric"
                        autoComplete="one-time-code"
                        maxLength={32}
                        value={mfaCode}
                        onChange={(e) => setMfaCode(e.target.value)}
                        className="tabular"
                      />
                    </Field>
                  </div>
                )}
                {problem?.field === "form" && <div className={FADE}><Callout tone="danger">{problem.message}</Callout></div>}
                <Button type="submit" loading={busy} className="w-full" size="lg">Change password</Button>
                <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
                  <button type="button" className="font-medium text-primary underline-offset-2 hover:underline disabled:opacity-50" disabled={busy}
                    onClick={() => void requestCode()}>
                    Send a new code
                  </button>
                  <button type="button" className="text-muted-foreground underline-offset-2 hover:underline" disabled={busy}
                    onClick={() => { setStep("request"); setProblem(null); setNeedsMfa(false); }}>
                    Use another email address
                  </button>
                </div>
                {requestErr && <div className={FADE}><Callout tone="danger">{requestErr}</Callout></div>}
              </form>
            )}

            {step === "done" && (
              <div className={cn("space-y-4", FADE)}>
                <Callout tone="success">
                  Your password was changed and every session of this account was signed out. A notice was sent to
                  your email address.
                </Callout>
                <Link href="/login" className={cn(buttonVariants({ size: "lg" }), "w-full")}>Sign in</Link>
              </div>
            )}
          </div>

          {step !== "done" && step !== "unavailable" && (
            <p className="px-1 text-center text-sm">
              <Link href="/login" className="inline-flex items-center gap-1 text-muted-foreground underline-offset-2 hover:underline">
                <ArrowLeft className="size-3.5" aria-hidden /> Back to sign in
              </Link>
            </p>
          )}
        </div>
      </main>

      <footer className="px-4 pb-6 text-center text-muted-foreground sm:px-6">
        <BySemantics />
      </footer>
    </div>
  );
}
