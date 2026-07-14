"use client";

import { useEffect, useState } from "react";
import { useRouter, usePathname } from "next/navigation";
import { Nav } from "@/components/nav";
import { api, Whoami } from "@/lib/api";
import { isPlatform } from "@/lib/console";

// Route guards: a Tenant user must not render Platform pages, and a Platform
// user must not render the Tenant "My Subscription" page. Backend authorization
// (403) is the real gate; this redirects to the correct shell for a clean UX.
const PLATFORM_ONLY = ["/customers", "/plan-catalog", "/licenses", "/fleet-health", "/enrollment"];
const TENANT_ONLY = ["/subscription", "/my-appliances"];

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [me, setMe] = useState<Whoami | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api.get<Whoami>("/v1/auth/whoami")
      .then((m) => setMe(m))
      .catch(() => router.replace("/login"))
      .finally(() => setLoading(false));
  }, [router]);

  // Enforce shell separation by path.
  useEffect(() => {
    if (loading || !me) return;
    const plat = isPlatform(me);
    if (!plat && PLATFORM_ONLY.some((p) => pathname.startsWith(p))) router.replace("/dashboard");
    if (plat && TENANT_ONLY.some((p) => pathname.startsWith(p))) router.replace("/dashboard");
  }, [pathname, me, loading, router]);

  async function onLogout() {
    try { await api.post("/v1/auth/logout"); } catch {}
    router.replace("/login");
    router.refresh();
  }

  if (loading) return <div className="p-8 text-muted text-sm">Loading…</div>;
  if (!me) return null;

  const platform = isPlatform(me);

  return (
    <div className="min-h-screen flex">
      <Nav email={me.email} onLogout={onLogout} platform={platform} />
      <main className="flex-1 min-w-0">
        {/* Scope header — always tells the operator which console/scope they are in. */}
        <div className="border-b border-border bg-panel px-6 py-2 text-xs text-muted">
          {platform
            ? "StayConnect Platform / Platform Administration"
            : "StayConnect / Group Administration"}
        </div>
        {children}
      </main>
    </div>
  );
}
