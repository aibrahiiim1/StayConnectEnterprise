"use client";

import { Suspense, useCallback, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { ShieldCheck } from "lucide-react";
import { BySemantics, OneGateMark, OneGateWordmark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ThemeToggle } from "@/components/theme-toggle";
import { api } from "@/lib/api";
import { safeNextPath } from "@/lib/safe-next";
import { SigninSecondStep, type SecondStep } from "@/components/signin-second-step";

export default function LoginPage() {
  // useSearchParams must live inside a Suspense boundary for production build.
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
  // The password is step one: it answers which second step comes and opens no session.
  const [second, setSecond] = useState<SecondStep | null>(null);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setLoading(true);
    try {
      const r = await api.post<{ second_step?: SecondStep }>("/auth/login", { email, password });
      setPassword("");
      setSecond(r.second_step === "enrol" ? "enrol" : "code");
    } catch (e) {
      setErr(e);
    } finally {
      setLoading(false);
    }
  }

  // A weak or admin-chosen password gets a session that can only replace it; the app layout shows that form on any page.
  const finish = useCallback(() => {
    router.replace(next);
    router.refresh();
  }, [router, next]);
  const restart = useCallback((why: string) => {
    setSecond(null);
    setErr(why ? new Error(why) : null);
  }, []);

  return (
    <div className="grid min-h-screen bg-background lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
      <aside className="relative hidden flex-col justify-between overflow-hidden bg-sidebar p-10 text-sidebar-foreground lg:flex">
        {/* "Gate" is black on every surface, so the wordmark sits on a white tile on the dark panel. */}
        <div className="relative z-10 flex flex-col items-start gap-3">
          <span className="inline-flex items-center rounded-xl bg-white px-5 py-3 shadow-control ring-1 ring-black/5">
            <OneGateWordmark className="text-[2.25rem]" />
          </span>
          <span className="text-nano uppercase tracking-[0.14em] text-sidebar-muted">Admin Console</span>
        </div>
        <div className="relative z-10 max-w-md space-y-4">
          <div className="text-micro uppercase tracking-[0.14em] text-sidebar-active">On-appliance console</div>
          <p className="text-[1.75rem] font-bold leading-tight tracking-[-0.02em] text-white">
            Client Wi-Fi for this site: who is online, how they got there, and whether everything is healthy.
          </p>
          <p className="text-sm leading-relaxed text-sidebar-muted">
            Runs on the appliance at the site and keeps working when the internet link or OneGate Central is
            unreachable.
          </p>
        </div>
        <BySemantics className="relative z-10 text-sidebar-muted" />
        <OneGateMark className="pointer-events-none absolute -right-24 -bottom-16 size-[26rem] opacity-[0.07]" />
      </aside>

      <div className="flex min-h-screen flex-col">
        <div className="flex items-center justify-end p-4 sm:p-6">
          <ThemeToggle />
        </div>

        <main className="flex flex-1 items-center justify-center px-4 pb-16 sm:px-6">
          <div className="w-full max-w-[25rem]">
            <div className="mb-7 space-y-3">
              <span className="inline-flex items-center rounded-lg bg-white px-3.5 py-2 shadow-control ring-1 ring-black/5">
                <OneGateWordmark className="text-[1.75rem]" />
              </span>
              <div className="space-y-1.5">
                {/* sr-only keeps the full product name in the heading's accessible name. */}
                <h1 className="text-title">
                  <span className="sr-only">OneGate </span>Admin Console
                </h1>
                <p className="text-sm text-muted-foreground">Sign in with your account for this site.</p>
              </div>
            </div>

            {/* role="status" (polite): informational, must not cut off the page's own announcement. */}
            <p role="status" className={expired ? "mb-4 rounded-lg border border-border bg-surface px-3.5 py-3 text-sm" : "sr-only"}>
              {expired ? "Your session ended because there was no activity for a while. Sign in again to continue where you left off." : ""}
            </p>

            {second ? (
              <SigninSecondStep step={second} onDone={finish} onRestart={restart} />
            ) : (
            <form onSubmit={onSubmit} className="space-y-4" noValidate={false}>
              {/* The banner is role="alert"; an aria-live wrapper would make screen readers read it twice. */}
              <ErrorBanner err={err} className="mb-0" />
              <Field label="Email or username">
                <Input
                  type="text"
                  required
                  // eslint-disable-next-line jsx-a11y/no-autofocus
                  autoFocus
                  autoComplete="username"
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
              <Button type="submit" disabled={loading} className="w-full" size="lg">
                {loading ? "Signing in…" : "Sign in"}
              </Button>
            </form>
            )}

            <div className="mt-6 flex gap-2.5 rounded-lg border border-border bg-card px-3.5 py-3 text-caption leading-relaxed text-muted-foreground">
              <ShieldCheck className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
              <p>
                This account is managed on this appliance. It is not a OneGate cloud account and does not work at
                any other site.
              </p>
            </div>
          </div>
        </main>

        <footer className="px-4 pb-6 text-center text-muted-foreground sm:px-6">
          <BySemantics />
        </footer>
      </div>

    </div>
  );
}
