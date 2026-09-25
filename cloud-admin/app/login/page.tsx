"use client";

// THE SIGN-IN SCREEN — outside the app shell, so it carries its own theme control.

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { KeyRound, ShieldCheck } from "lucide-react";
import { OneGateLockup, OneGateMark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ThemeToggle } from "@/components/theme-toggle";
import { api, ListResp } from "@/lib/api";

type SSOProvider = { name: string; display_name: string; kind: string };

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
  // NO DEFAULT ORG SLUG. It exists ONLY to look up which SSO providers an organisation has configured; normal
  // email/password sign-in ignores it. It starts empty and the SSO block stays collapsed until asked for.
  const [tenantSlug, setTenantSlug] = useState("");
  const [ssoOpen, setSsoOpen] = useState(false);
  const [providers, setProviders] = useState<SSOProvider[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  // Fetch the organisation's SSO providers whenever the slug changes (debounced).
  useEffect(() => {
    if (!tenantSlug) { setProviders([]); return; }
    const t = setTimeout(() => {
      api.get<ListResp<SSOProvider>>(`/v1/auth/sso/providers?tenant=${encodeURIComponent(tenantSlug)}`)
        .then((r) => setProviders(r.data ?? []))
        .catch(() => setProviders([]));
    }, 200);
    return () => clearTimeout(t);
  }, [tenantSlug]);

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

  function ssoStartHref(p: SSOProvider): string {
    const q = new URLSearchParams({ tenant: tenantSlug, provider: p.name, return_to: next });
    return `/api/v1/auth/sso/start?${q.toString()}`;
  }

  return (
    <div className="grid min-h-screen bg-background lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
      <aside className="relative hidden flex-col justify-between overflow-hidden bg-sidebar p-10 text-sidebar-foreground lg:flex">
        <OneGateLockup product="Central" inverse />
        <div className="relative z-10 max-w-md space-y-4">
          <div className="text-micro uppercase tracking-[0.14em] text-sidebar-active">Vendor console</div>
          <p className="text-[1.75rem] font-bold leading-tight tracking-[-0.02em] text-white">
            Customers, sites and appliances: activate each appliance and manage its license for its lifetime.
          </p>
          <p className="text-sm leading-relaxed text-sidebar-muted">
            Central is used for licensing only. Each hotel&apos;s network and guests are run from Hotel Admin on its
            own appliance.
          </p>
        </div>
        <div className="text-caption text-sidebar-muted">OneGate · Central</div>
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

            {/* SINGLE SIGN-ON, collapsed by default and clearly scoped to SSO alone. */}
            <div className="mt-6 border-t border-border pt-5">
              {!ssoOpen ? (
                <button
                  type="button"
                  onClick={() => setSsoOpen(true)}
                  aria-expanded={false}
                  className="inline-flex items-center gap-1.5 rounded text-sm text-muted-foreground underline underline-offset-2 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <KeyRound className="size-3.5" aria-hidden />
                  Use single sign-on instead
                </button>
              ) : (
                <div className="space-y-3">
                  <Field
                    label="Organisation slug"
                    hint="Only used to look up your organisation's single sign-on providers. It is not your hotel, site or appliance, and email & password sign-in above ignores it."
                  >
                    <Input
                      value={tenantSlug}
                      onChange={(e) => setTenantSlug(e.target.value.trim().toLowerCase())}
                      placeholder="your-organisation"
                      autoFocus
                    />
                  </Field>
                  <div aria-live="polite">
                    {tenantSlug === "" ? (
                      <p className="text-caption text-muted-foreground">
                        Enter your organisation slug to see its sign-on providers.
                      </p>
                    ) : providers.length === 0 ? (
                      <p className="text-caption text-muted-foreground">
                        No single sign-on is configured for <span className="font-mono">{tenantSlug}</span>. Use
                        email and password above.
                      </p>
                    ) : (
                      <div className="space-y-2">
                        {providers.map((p) => (
                          <a
                            key={p.name}
                            href={ssoStartHref(p)}
                            className="flex h-10 items-center justify-center rounded-md border border-foreground/85 bg-card px-4 text-[0.8125rem] font-semibold text-foreground transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                          >
                            Sign in with {p.display_name}
                          </a>
                        ))}
                      </div>
                    )}
                  </div>
                  <button
                    type="button"
                    onClick={() => setSsoOpen(false)}
                    className="rounded text-sm text-muted-foreground underline underline-offset-2 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    Back to email sign-in
                  </button>
                </div>
              )}
            </div>

            <div className="mt-6 flex gap-2.5 rounded-lg border border-border bg-card px-3.5 py-3 text-caption leading-relaxed text-muted-foreground">
              <ShieldCheck className="mt-0.5 size-4 shrink-0" aria-hidden />
              <p>
                A Central account signs in to this console only. It opens nothing on a hotel&apos;s appliance, and a
                Hotel Admin account does not work here.
              </p>
            </div>
          </div>
        </main>
      </div>
    </div>
  );
}
