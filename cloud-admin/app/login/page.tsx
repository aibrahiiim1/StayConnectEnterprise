"use client";

// THE SIGN-IN SCREEN — outside the app shell, so it carries its own theme control.

import { Suspense, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { ShieldCheck } from "lucide-react";
import { BySemantics, OneGateLockup, OneGateMark } from "@/components/brand";
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
  // Only a path inside this console: "next" arrives in the address and must not send anyone elsewhere.
  const asked = params.get("next") ?? "";
  const next = asked.startsWith("/") && !asked.startsWith("//") ? asked : "/overview";

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setLoading(true);
    try {
      await api.post("/v1/auth/login", { email, password });
      router.replace(next);
      router.refresh();
    } catch (e: any) {
      setErr(e?.message || "Login failed");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="grid min-h-screen bg-background lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
      <aside className="relative hidden flex-col justify-between overflow-hidden bg-sidebar p-10 text-sidebar-foreground lg:flex">
        <OneGateLockup product="Central" inverse />
        <div className="relative z-10 max-w-md space-y-4">
          <p className="text-[1.75rem] font-bold leading-tight tracking-[-0.02em] text-white">
            Customers, sites and appliances: activate each appliance and manage its license for its lifetime.
          </p>
          <p className="text-sm leading-relaxed text-sidebar-muted">
            Central is used for licensing only. Each site&apos;s network and clients are run from the Admin Console on its
            own appliance.
          </p>
        </div>
        <BySemantics className="relative z-10 text-sidebar-muted" />
        <OneGateMark className="pointer-events-none absolute -bottom-16 -end-24 size-[26rem] opacity-[0.07]" />
      </aside>

      <div className="flex min-h-screen flex-col">
        <div className="flex items-center justify-between p-4 sm:p-6">
          <div className="lg:invisible">
            <OneGateLockup product="Central" />
          </div>
          <ThemeToggle />
        </div>

        <main className="flex flex-1 items-center justify-center px-4 pb-16 sm:px-6">
          <div className="w-full max-w-[25rem]">
            <div className="mb-7 space-y-1.5">
              <OneGateMark className="mb-3 size-10" />
              <h1 className="text-title">OneGate Central</h1>
              <p className="text-sm text-muted-foreground">Admin sign-in. Use your Central operator account.</p>
            </div>

            <form onSubmit={onSubmit} className="space-y-4">
              <div aria-live="assertive">
                <ErrorBanner err={err} className="mb-0" />
              </div>
              <Field label="Email">
                <Input
                  type="email"
                  required
                  autoFocus
                  autoComplete="email"
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
              <ShieldCheck className="mt-0.5 size-4 shrink-0" aria-hidden />
              <p>
                A Central account signs in to this console only. It opens nothing on a site&apos;s appliance, and an
                Admin Console account does not work here.
              </p>
            </div>

            <p className="mt-8 text-center">
              <BySemantics className="text-muted-foreground" />
            </p>
          </div>
        </main>
      </div>
    </div>
  );
}
