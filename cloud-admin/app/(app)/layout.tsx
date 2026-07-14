"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Nav } from "@/components/nav";
import { api, Whoami } from "@/lib/api";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const [me, setMe] = useState<Whoami | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    api.get<Whoami>("/v1/auth/whoami")
      .then((m) => { if (!cancelled) setMe(m); })
      .catch(() => { if (!cancelled) router.replace("/login"); })
      .finally(() => { if (!cancelled) setLoading(false); });
    // Re-validate periodically so a session that expires while watching a
    // long-lived page (e.g. the onboarding wizard's auto-run) recovers to
    // /login instead of erroring on every poll.
    const iv = setInterval(() => {
      api.get<Whoami>("/v1/auth/whoami").catch(() => { if (!cancelled) router.replace("/login"); });
    }, 30000);
    return () => { cancelled = true; clearInterval(iv); };
  }, [router]);

  async function onLogout() {
    try { await api.post("/v1/auth/logout"); } catch {}
    router.replace("/login");
    router.refresh();
  }

  if (loading) return <div className="p-8 text-muted text-sm">Loading…</div>;
  if (!me) return null;

  return (
    <div className="min-h-screen flex">
      <Nav email={me.email} onLogout={onLogout} />
      <main className="flex-1 min-w-0">{children}</main>
    </div>
  );
}
