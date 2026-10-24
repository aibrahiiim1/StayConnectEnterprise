"use client";

import { Suspense, useCallback, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { BySemantics, OneGateLockup } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { ThemeToggle } from "@/components/theme-toggle";
import { api } from "@/lib/api";
import { destinationAfterSignIn } from "@/lib/session-expiry";
import { SigninSecondStep } from "@/components/signin-second-step";

export default function LoginPage() {
  // Next.js requires Suspense around useSearchParams in production builds.
  return (
    <Suspense fallback={null}>
      <LoginInner />
    </Suspense>
  );
}

function LoginInner() {
  const router = useRouter();
  const params = useSearchParams();
  // Reject external return paths supplied in the address bar.
  const next = destinationAfterSignIn(params.get("next"));
  const expired = params.get("reason") === "expired";

  useEffect(() => { document.title = "Sign in · OneGate Central"; }, []);
  // Fill the mounted live region after render so screen readers announce the sign-out reason.
  const [notice, setNotice] = useState("");
  useEffect(() => {
    setNotice(expired ? "Your sign-in ended, so you were signed out. Sign in again to continue where you were." : "");
  }, [expired]);

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  // With two-step sign-in on, the password opens no session: the code step does.
  const [codeStep, setCodeStep] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setLoading(true);
    try {
      const r = await api.post<{ second_step?: "none" | "code" }>("/v1/auth/login", { email, password });
      setPassword("");
      if (r.second_step === "none") finish();
      else setCodeStep(true);
    } catch (e: any) {
      setErr(e?.message || "Login failed");
    } finally {
      setLoading(false);
    }
  }

  const finish = useCallback(() => {
    router.replace(next);
    router.refresh();
  }, [router, next]);
  const restart = useCallback((why: string) => {
    setCodeStep(false);
    setErr(why || null);
  }, []);

  return (
    <div className="flex min-h-screen flex-col bg-background">
      <header className="flex items-center justify-between gap-3 px-4 py-4 sm:px-6">
        <OneGateLockup product="Central" />
        <ThemeToggle />
      </header>

      <main className="flex flex-1 items-start justify-center px-4 pb-12 pt-6 sm:items-center sm:px-6 sm:pt-0">
        <div className="w-full max-w-[24rem] space-y-4">
          <div className="rounded-xl border border-border bg-card p-6 text-card-foreground shadow-card sm:p-7">
            <div className="mb-6 space-y-1">
              <h1 className="text-title">OneGate Central</h1>
              <p className="text-sm text-muted-foreground">
                {codeStep ? "One more step to finish signing in." : "Sign in with your Central account."}
              </p>
            </div>

            {codeStep ? (
              <SigninSecondStep onDone={finish} onRestart={restart} />
            ) : (
              <form onSubmit={onSubmit} className="space-y-4">
                {/* The alert already announces itself; an outer live wrapper would announce it twice. */}
                <ErrorBanner err={err} className="mb-0" />
                <div role="status" aria-live="polite">
                  {notice && !err && <Callout tone="neutral">{notice}</Callout>}
                </div>
                <Field label="Email">
                  <Input
                    type="email"
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
            A Central account signs in to this console only. It opens nothing on a site&apos;s appliance, and an Admin
            Console account does not work here.
          </p>
        </div>
      </main>

      <footer className="px-4 pb-6 text-center text-muted-foreground sm:px-6">
        <BySemantics />
      </footer>
    </div>
  );
}
