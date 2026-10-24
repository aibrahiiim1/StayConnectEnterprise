"use client";

import { Suspense, useCallback, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { BySemantics, OneGateLockup } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { ThemeToggle } from "@/components/theme-toggle";
import { api } from "@/lib/api";
import { safeNextPath } from "@/lib/safe-next";
import { SigninSecondStep } from "@/components/signin-second-step";

export default function LoginPage() {
  // useSearchParams must live inside a Suspense boundary for the production build.
  return (
    <Suspense fallback={null}>
      <LoginInner />
    </Suspense>
  );
}

function LoginInner() {
  const router = useRouter();
  const params = useSearchParams();
  // Only a path on this console: an external ?next= would hand the sign-in off to another site.
  const next = safeNextPath(params.get("next"));
  const expired = params.get("reason") === "expired";

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  // An account with two-step sign-in on gets no session from the password: the code step opens it.
  const [codeStep, setCodeStep] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setLoading(true);
    try {
      const r = await api.post<{ second_step?: "none" | "code" }>("/auth/login", { email, password });
      setPassword("");
      if (r.second_step === "none") finish();
      else setCodeStep(true);
    } catch (e) {
      setErr(e);
    } finally {
      setLoading(false);
    }
  }

  // A weak or admin-chosen password gets a session that can only replace it; the app layout shows that form.
  const finish = useCallback(() => {
    router.replace(next);
    router.refresh();
  }, [router, next]);
  const restart = useCallback((why: string) => {
    setCodeStep(false);
    setErr(why ? new Error(why) : null);
  }, []);

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
              {/* sr-only keeps the full product name in the heading's accessible name. */}
              <h1 className="text-title">
                <span className="sr-only">OneGate </span>Admin Console
              </h1>
              <p className="text-sm text-muted-foreground">
                {codeStep ? "One more step to finish signing in." : "Sign in with your account for this site."}
              </p>
            </div>

            {/* role="status" (polite): informational, must not cut off the page's own announcement. */}
            <div role="status" className={expired && !codeStep ? "mb-4" : "sr-only"}>
              {expired && !codeStep ? (
                <Callout tone="neutral">
                  Your session ended because there was no activity for a while. Sign in again to continue where you
                  left off.
                </Callout>
              ) : null}
            </div>

            {codeStep ? (
              <SigninSecondStep onDone={finish} onRestart={restart} />
            ) : (
              <form onSubmit={onSubmit} className="space-y-4">
                {/* The banner is role="alert"; an aria-live wrapper would make screen readers read it twice. */}
                <ErrorBanner err={err} className="mb-0" />
                <Field label="Email or username">
                  <Input
                    type="text"
                    required
                    // eslint-disable-next-line jsx-a11y/no-autofocus
                    autoFocus
                    autoComplete="username"
                    autoCapitalize="none"
                    spellCheck={false}
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                  />
                </Field>
                <Field label="Password">
                  <Input
                    type="password"
                    required
                    autoComplete="current-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </Field>
                <Button type="submit" loading={loading} className="w-full" size="lg">
                  Sign in
                </Button>
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
