"use client";

// THE SIGN-IN SCREEN — the first thing anyone sees, and the only screen outside the app shell.
//
// It has no sidebar and no top bar, so the theme control has to be here too: an operator whose appliance is set
// to dark would otherwise meet a light login page and conclude the theme preference had not stuck.

import { Suspense, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { ShieldCheck } from "lucide-react";
import { BySemantics, OneGateMark, OneGateWordmark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ThemeToggle } from "@/components/theme-toggle";
import { api } from "@/lib/api";

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
  const next = params.get("next") || "/dashboard";

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setLoading(true);
    try {
      // POST /edge/v1/auth/login — local site operators only; the appliance
      // has no org concept and no SSO.
      await api.post("/auth/login", { email, password });
      router.replace(next);
      router.refresh();
    } catch (e) {
      setErr(e);
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="grid min-h-screen bg-background lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
      {/* THE INVERSE PANEL. Desktop only: it carries the product identity and the one fact an operator needs
          before typing a password here -- that this login belongs to this building. Phones get the form. */}
      <aside className="relative hidden flex-col justify-between overflow-hidden bg-sidebar p-10 text-sidebar-foreground lg:flex">
        {/* The wordmark, large, on its light tile: "Gate" is black on every surface, so on this dark panel the
            mark sits on white rather than changing colour (components/brand.tsx). */}
        <div className="relative z-10 flex flex-col items-start gap-3">
          <span className="inline-flex items-center rounded-xl bg-white px-5 py-3 shadow-control ring-1 ring-black/5">
            <OneGateWordmark className="text-[2.25rem]" />
          </span>
          <span className="text-nano uppercase tracking-[0.14em] text-sidebar-muted">Hotel Admin</span>
        </div>
        <div className="relative z-10 max-w-md space-y-4">
          <div className="text-micro uppercase tracking-[0.14em] text-sidebar-active">On-appliance console</div>
          <p className="text-[1.75rem] font-bold leading-tight tracking-[-0.02em] text-white">
            Guest Wi-Fi for this property: who is online, how they got there, and whether everything is healthy.
          </p>
          <p className="text-sm leading-relaxed text-sidebar-muted">
            Runs on the appliance in the hotel and keeps working when the internet link or OneGate Central is
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
                {/* The wordmark above already says OneGate; the heading's accessible name keeps the full product
                    name for assistive technology and for anything that finds the page by it. */}
                <h1 className="text-title">
                  <span className="sr-only">OneGate </span>Hotel Admin
                </h1>
                <p className="text-sm text-muted-foreground">Sign in with your account for this property.</p>
              </div>
            </div>

            <form onSubmit={onSubmit} className="space-y-4" noValidate={false}>
              <div aria-live="assertive">
                <ErrorBanner err={err} className="mb-0" />
              </div>
              <Field label="Email or username">
                <Input
                  type="text"
                  required
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

            <div className="mt-6 flex gap-2.5 rounded-lg border border-border bg-card px-3.5 py-3 text-caption leading-relaxed text-muted-foreground">
              <ShieldCheck className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
              <p>
                This account is managed on this appliance. It is not a OneGate cloud account and does not work at
                any other property.
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
