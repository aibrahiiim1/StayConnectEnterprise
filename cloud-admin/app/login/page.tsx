"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
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

  const [email, setEmail] = useState("admin@stayconnect.local");
  const [password, setPassword] = useState("");
  // NO DEFAULT ORG SLUG.
  //
  // This defaulted to "dev", which put the word "dev" on the Production sign-in screen and read as an
  // environment or tenant selector. It is neither: normal email/password sign-in ignores it entirely, and it
  // exists ONLY to look up which SSO providers an organisation has configured. Operators reasonably mistook
  // it for the appliance Tenant/Site selection.
  //
  // It now starts empty and the whole SSO block stays collapsed until someone asks for it, so the default
  // sign-in screen is exactly two fields. The SSO capability itself is unchanged.
  const [tenantSlug, setTenantSlug] = useState("");
  const [ssoOpen, setSsoOpen] = useState(false);
  const [providers, setProviders] = useState<SSOProvider[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  // Fetch the tenant's SSO providers whenever the slug changes (debounced).
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
    <div className="min-h-screen flex items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <div>
            <div className="text-xs text-muted uppercase tracking-widest">StayConnect</div>
            <CardTitle>Admin sign-in</CardTitle>
          </div>
        </CardHeader>
        <CardBody>
          <form onSubmit={onSubmit} className="space-y-4">
            <div>
              <Label htmlFor="email">Email</Label>
              <Input
                id="email" type="email" required autoFocus autoComplete="email"
                value={email} onChange={(e) => setEmail(e.target.value)}
              />
            </div>
            <div>
              <Label htmlFor="pw">Password</Label>
              <Input
                id="pw" type="password" required autoComplete="current-password"
                value={password} onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            {err && <div className="text-err text-sm">{err}</div>}
            <Button type="submit" disabled={loading} className="w-full">
              {loading ? "Signing in…" : "Sign in"}
            </Button>
          </form>

          {/* SINGLE SIGN-ON, collapsed by default and clearly scoped to SSO alone. */}
          <div className="mt-6 pt-4 border-t border-border">
            {!ssoOpen ? (
              <button
                type="button"
                onClick={() => setSsoOpen(true)}
                className="text-xs text-muted hover:text-fg underline underline-offset-2"
              >
                Use single sign-on instead
              </button>
            ) : (
              <div className="space-y-3">
                <div>
                  <Label htmlFor="tenant">Organisation slug</Label>
                  <p className="text-xs text-muted mt-1 mb-1.5">
                    Only used to look up your organisation&apos;s single sign-on providers. It is not your
                    hotel, site or appliance, and email &amp; password sign-in above ignores it.
                  </p>
                  <Input
                    id="tenant"
                    value={tenantSlug}
                    onChange={(e) => setTenantSlug(e.target.value.trim().toLowerCase())}
                    placeholder="your-organisation"
                    autoFocus
                  />
                </div>
                {tenantSlug === "" ? (
                  <div className="text-xs text-muted">
                    Enter your organisation slug to see its sign-on providers.
                  </div>
                ) : providers.length === 0 ? (
                  <div className="text-xs text-muted">
                    No single sign-on is configured for{" "}
                    <span className="font-mono">{tenantSlug}</span>. Use email and password above.
                  </div>
                ) : (
                  <div className="space-y-2">
                    {providers.map((p) => (
                      <a
                        key={p.name}
                        href={ssoStartHref(p)}
                        className="block text-center px-4 py-2 rounded-md border border-border bg-panel2 hover:bg-[#222735] text-sm font-medium"
                      >
                        Sign in with {p.display_name}
                      </a>
                    ))}
                  </div>
                )}
                <button
                  type="button"
                  onClick={() => setSsoOpen(false)}
                  className="text-xs text-muted hover:text-fg underline underline-offset-2"
                >
                  Back to email sign-in
                </button>
              </div>
            )}
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
