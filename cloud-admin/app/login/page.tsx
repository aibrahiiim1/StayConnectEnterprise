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
  const [tenantSlug, setTenantSlug] = useState("dev");
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

          {/* SSO section. The tenant slug is a per-org disambiguator until we
              have email-domain-based discovery. */}
          <div className="mt-6 pt-4 border-t border-border space-y-3">
            <Label htmlFor="tenant">Org slug</Label>
            <Input
              id="tenant"
              value={tenantSlug}
              onChange={(e) => setTenantSlug(e.target.value.trim().toLowerCase())}
              placeholder="acme"
            />
            {providers.length === 0 ? (
              <div className="text-xs text-muted">
                No single sign-on configured for <span className="font-mono">{tenantSlug || "—"}</span>.
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
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
