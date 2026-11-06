"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
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
    if (e.code === "forbidden" && /no roles/i.test(e.message)) {
      return new Error("This account has no role yet. Ask an administrator to give it one.");
    }
  }
  return e;
}

type PasswordBoxProps = {
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  value: string;
  onChange: (v: string) => void;
  onCapsLock: (on: boolean) => void;
  inputRef: React.RefObject<HTMLInputElement>;
};

function PasswordBox({ id, value, onChange, onCapsLock, inputRef, ...aria }: PasswordBoxProps) {
  const [shown, setShown] = useState(false);
  const caps = (e: React.KeyboardEvent<HTMLInputElement>) => onCapsLock(e.getModifierState?.("CapsLock") ?? false);
  return (
    <div className="relative">
      <Input
        ref={inputRef}
        id={id}
        aria-describedby={aria["aria-describedby"]}
        aria-invalid={aria["aria-invalid"]}
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
  const emailRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);

  const [resetOffered, setResetOffered] = useState(false);

  useEffect(() => {
    if (!codeStep) emailRef.current?.focus();
  }, [codeStep]);

  useEffect(() => {
    let live = true;
    Promise.resolve()
      .then(() => api.get<{ available?: boolean }>("/auth/password-reset"))
      .then((r) => { if (live) setResetOffered(r?.available === true); })
      .catch(() => { if (live) setResetOffered(false); });
    return () => { live = false; };
  }, []);

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
    setErr(null);
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
      setErr(signinWords(e));
      setPassword("");
      requestAnimationFrame(() => passwordRef.current?.focus());
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      <header className="flex items-center justify-between gap-3 px-4 py-4 sm:px-6">
        <OneGateLockup product="Admin Console" animated />
        <ThemeToggle />
      </header>

      <main className="flex flex-1 items-start justify-center px-4 pb-12 pt-6 sm:items-center sm:px-6 sm:pt-0">
        <div className="w-full max-w-[24rem] space-y-4">
          <div className="rounded-xl border border-border bg-card p-6 text-card-foreground shadow-card sm:p-7">
            <div className="mb-6 space-y-1">
              <h1 className="text-title">
                <span className="sr-only">OneGate </span>Admin Console
              </h1>
              <p className="text-sm text-muted-foreground">
                {codeStep ? "One more step to finish signing in." : "Sign in with your account for this site."}
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
                    value={password}
                    onChange={(v) => { setPassword(v); setPasswordErr(null); }}
                    onCapsLock={setCapsOn}
                  />
                </Field>
                {err ? (
                  <div key={String((err as { message?: string })?.message ?? err)} className={FADE}>
                    <ErrorBanner err={err} className="mb-0" />
                  </div>
                ) : null}
                <Button type="submit" loading={loading} className="w-full" size="lg">
                  Sign in
                </Button>
                {resetOffered && (
                  <p className={cn("text-center text-sm", FADE)}>
                    <Link
                      href="/forgot-password"
                      className="font-medium text-primary underline-offset-2 hover:underline"
                    >
                      Forgot your password?
                    </Link>
                  </p>
                )}
              </form>
            )}
          </div>

          <p className="px-1 text-center text-caption text-muted-foreground">
            A local account for this appliance only. It is not a OneGate cloud account and does not work at any other
            site.
          </p>
        </div>
      </main>

      <footer className="px-4 pb-6 text-center text-muted-foreground sm:px-6">
        <BySemantics />
      </footer>
    </div>
  );
}
