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
    (async () => {
      try {
        const m = await api.get<Whoami>("/auth/whoami");
        if (!cancelled) setMe(m);
      } catch {
        // whoami failed → the session cookie is stale/invalid (e.g. edged
        // restarted and dropped its in-memory sessions). Explicitly clear the
        // cookie so the middleware won't bounce /login back to /dashboard (a
        // redirect loop), then show the login form.
        try { await api.post("/auth/logout"); } catch {}
        if (!cancelled) router.replace("/login");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, [router]);

  async function onLogout() {
    try { await api.post("/auth/logout"); } catch {}
    router.replace("/login");
    router.refresh();
  }

  if (loading) return <div className="p-8 text-muted text-sm">Loading…</div>;
  if (!me) return null;

  return (
    <div className="min-h-screen flex">
      <Nav email={me.email} roles={me.roles ?? []} onLogout={onLogout} />
      <main className="flex-1 min-w-0">{children}</main>
    </div>
  );
}
