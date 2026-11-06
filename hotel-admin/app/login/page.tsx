"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Eye, EyeOff } from "lucide-react";
import { BySemantics, OneGateLockup } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { ThemeToggle } from "@/components/theme-toggle";
import { api, ApiError } from "@/lib/api";
import { safeNextPath } from "@/lib/safe-next";
import { SigninSecondStep } from "@/components/signin-second-step";
import { cn } from "@/lib/utils";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

function signinWords(e: unknown): unknown {
  if (e instanceof ApiError) {
    if (e.code === "unauthenticated") {
      return new Error("The email, username or password is not right. Check them and try again.");
    }
    if (e.code === "no_roles" || (e.code === "forbidden" && /no roles/i.test(e.message))) {
      return new Error("This account has no role yet. Ask an administrator to give it one.");
    }
  }
  return e;
}

function lockoutSeconds(e: unknown): number | null {
  if (!(e instanceof ApiError) || (e.status !== 429 && e.code !== "too_many_attempts")) return null;
  const s = Number(e.body?.retry_after_seconds);
  return Number.isFinite(s) && s > 0 ? Math.ceil(s) : 0;
}

function busyWords(e: unknown): string | null {
  if (!(e instanceof ApiError)) return null;
  if (e.code === "signin_unavailable" || e.code === "signin_busy") return e.message;
  if (e.status === 502 || e.status === 503 || e.status === 504) return "Sign-in is unavailable right now. Try again shortly.";
  return null;
}

function clockWords(sec: number): string {
  const m = Math.floor(sec / 60);
  const r = sec % 60;
  return `${m}:${String(r).padStart(2, "0")}`;
}

type PasswordBoxProps = {
  id?: string;
  readOnly?: boolean;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  value: string;
  onChange: (v: string) => void;
  onCapsLock: (on: boolean) => void;
  inputRef: React.RefObject<HTMLInputElement>;
};

function PasswordBox({ id, readOnly, value, onChange, onCapsLock, inputRef, ...aria }: PasswordBoxProps) {
  const [shown, setShown] = useState(false);
  const caps = (e: React.KeyboardEvent<HTMLInputElement>) => onCapsLock(e.getModifierState?.("CapsLock") ?? false);
  return (
    <div className="relative">
      <Input
        ref={inputRef}
        id={id}
        aria-describedby={aria["aria-describedby"]}
        aria-invalid={aria["aria-invalid"]}
        readOnly={readOnly}
        type={shown ? "text" : "password"}
        autoComplete="current-password"
        autoCapitalize="none"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={caps}
        onKeyUp={caps}
        onBlur={() => onCapsLock(false)}
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

export default function LoginPage() {
  return (
    <Suspense fallback={null}>
      <LoginInner />
    </Suspense>
  );
}

function LoginInner() {
  const router = useRouter();
  const params = useSearchParams();
  const next = safeNextPath(params.get("next"));
  const expired = params.get("reason") === "expired";

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [emailErr, setEmailErr] = useState<string | null>(null);
  const [passwordErr, setPasswordErr] = useState<string | null>(null);
  const [capsOn, setCapsOn] = useState(false);
  const [loading, setLoading] = useState(false);
  const [codeStep, setCodeStep] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [lockedUntil, setLockedUntil] = useState<number | null>(null);
  const [lockText, setLockText] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const emailRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!codeStep) emailRef.current?.focus();
  }, [codeStep]);

  useEffect(() => {
    if (lockedUntil === null) return;
    const t = window.setInterval(() => {
      const n = Date.now();
      setNow(n);
      if (n >= lockedUntil) {
        setLockedUntil(null);
        setLockText(null);
        passwordRef.current?.focus();
      }
    }, 1000);
    return () => window.clearInterval(t);
  }, [lockedUntil]);

  const lockLeft = lockedUntil === null ? 0 : Math.max(0, Math.ceil((lockedUntil - now) / 1000));

  const finish = useCallback(() => {
    router.replace(next);
    router.refresh();
  }, [router, next]);
  const restart = useCallback((why: string) => {
    setCodeStep(false);
    setErr(why ? new Error(why) : null);
  }, []);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (loading || lockedUntil !== null) return;
    setErr(null);
    setBusy(null);
    setLockText(null);
    const noEmail = email.trim() === "";
    const noPassword = password === "";
    setEmailErr(noEmail ? "Enter your email or username." : null);
    setPasswordErr(noPassword ? "Enter your password." : null);
    if (noEmail) { emailRef.current?.focus(); return; }
    if (noPassword) { passwordRef.current?.focus(); return; }
    setLoading(true);
    try {
      const r = await api.post<{ second_step?: "none" | "code" }>("/auth/login", { email, password });
      setPassword("");
      if (r.second_step === "none") finish();
      else setCodeStep(true);
    } catch (e) {
      const wait = lockoutSeconds(e);
      const busyText = busyWords(e);
      if (wait !== null) {
        const t = Date.now();
        setNow(t);
        if (wait > 0) setLockedUntil(t + wait * 1000);
        setLockText(e instanceof Error && e.message ? e.message : "Too many failed sign-in attempts. Wait a few minutes and try again.");
      } else if (busyText) {
        setBusy(busyText);
      } else {
        setErr(signinWords(e));
      }
      setPassword("");
      requestAnimationFrame(() => passwordRef.current?.focus());
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      <header className="flex items-center justify-between gap-3 px-4 py-4 sm:px-6">
        <OneGateLockup animated />
        <ThemeToggle />
      </header>

      <main className="flex flex-1 items-start justify-center px-4 pb-12 pt-6 sm:items-center sm:px-6 sm:pt-0">
        <div className="w-full max-w-[24rem] space-y-4">
          <div className="rounded-xl border border-border bg-card p-6 text-card-foreground shadow-card sm:p-7">
            <div className="mb-6 space-y-1">
              <h1 className="text-title">Sign in</h1>
              <p className="text-sm text-muted-foreground">
                {codeStep ? "One more step to finish signing in." : "Use your account for this site."}
              </p>
            </div>

            <div role="status" className={expired && !codeStep ? "mb-4" : "sr-only"}>
              {expired && !codeStep ? (
                <Callout tone="neutral">
                  Your session ended because there was no activity for a while. Sign in again to continue where you
                  left off.
                </Callout>
              ) : null}
            </div>

            {codeStep ? (
              <div className={FADE}>
                <SigninSecondStep onDone={finish} onRestart={restart} />
              </div>
            ) : (
              <form onSubmit={onSubmit} className={cn("space-y-4", FADE)} noValidate>
                <Field label="Email or username" error={emailErr ?? undefined}>
                  <Input
                    ref={emailRef}
                    type="text"
                    autoComplete="username"
                    readOnly={loading}
                    autoCapitalize="none"
                    spellCheck={false}
                    value={email}
                    onChange={(e) => { setEmail(e.target.value); setEmailErr(null); }}
                  />
                </Field>
                <Field
                  label="Password"
                  error={passwordErr ?? undefined}
                  hint={capsOn && !passwordErr ? "Caps Lock is on." : undefined}
                >
                  <PasswordBox
                    inputRef={passwordRef}
                    readOnly={loading}
                    value={password}
                    onChange={(v) => { setPassword(v); setPasswordErr(null); }}
                    onCapsLock={setCapsOn}
                  />
                </Field>
                {lockText ? (
                  <Callout tone="warning" title="Sign-in paused" className={FADE}>
                    {lockText}
                  </Callout>
                ) : null}
                {busy ? (
                  <Callout tone="neutral" title="Sign-in is busy" className={FADE}>
                    {busy}
                  </Callout>
                ) : null}
                {err ? (
                  <div key={String((err as { message?: string })?.message ?? err)} className={FADE}>
                    <ErrorBanner err={err} className="mb-0" />
                  </div>
                ) : null}
                <Button type="submit" loading={loading} disabled={lockedUntil !== null} className="w-full" size="lg">
                  {lockedUntil !== null ? `Try again in ${clockWords(lockLeft)}` : "Sign in"}
                </Button>
              </form>
            )}
          </div>

        </div>
      </main>

      <footer className="px-4 pb-6 text-center text-muted-foreground sm:px-6">
        <BySemantics />
      </footer>
    </div>
  );
}
