"use client";

import { useEffect, useRef, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import { Nav } from "@/components/nav";
import { api, Whoami } from "@/lib/api";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const mainRef = useRef<HTMLElement | null>(null);
  const [me, setMe] = useState<Whoami | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    const bounce = async () => {
      // Session cookie is stale/invalid (expired, or edged restarted and dropped
      // its in-memory sessions). Explicitly clear the cookie so the middleware
      // won't bounce /login back to /dashboard (a redirect loop), then show the
      // login form.
      try { await api.post("/auth/logout"); } catch {}
      if (!cancelled) router.replace("/login");
    };
    (async () => {
      try {
        const m = await api.get<Whoami>("/auth/whoami");
        if (!cancelled) setMe(m);
      } catch {
        await bounce();
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    // Re-validate periodically so a session that expires while the operator is
    // watching a long-lived page (onboarding, sessions, dashboard) recovers to
    // /login instead of every poll erroring on 401.
    const iv = setInterval(() => {
      api.get<Whoami>("/auth/whoami").catch(() => bounce());
    }, 30000);
    return () => { cancelled = true; clearInterval(iv); };
  }, [router]);

  // With the CONTENT as the scrolling element, the window no longer scrolls, so Next's scroll-to-top on
  // navigation has nothing to reset. Reset the content pane explicitly instead -- otherwise an operator who
  // scrolled to the bottom of one screen would open the next one already scrolled halfway down it.
  useEffect(() => { mainRef.current?.scrollTo({ top: 0 }); }, [pathname]);

  async function onLogout() {
    try { await api.post("/auth/logout"); } catch {}
    router.replace("/login");
    router.refresh();
  }

  if (loading) return <div className="p-8 text-muted text-sm">Loading…</div>;
  if (!me) return null;

  return (
    // THE SIDEBAR "JUMPING BACK TO THE TOP" WAS THE WINDOW SCROLLING, NOT THE MENU MOVING.
    //
    // `min-h-screen` is a MINIMUM: on a long screen the flex container grew past the viewport, the sidebar
    // stretched with it, and its own `overflow-y-auto` never engaged -- so reaching a lower item such as
    // WAN / LAN settings meant scrolling the WHOLE WINDOW down. Clicking it then navigated, Next.js reset the
    // window to the top as it is supposed to, and the menu appeared to snap back.
    //
    // `h-screen` + `overflow-hidden` bounds the container to the viewport. The sidebar becomes a real
    // independently-scrolling column whose position survives navigation (the layout is not remounted between
    // routes), and the page content scrolls in its own pane.
    <div className="h-screen flex overflow-hidden">
      <Nav email={me.email} roles={me.roles ?? []} onLogout={onLogout} />
      <main ref={mainRef} className="flex-1 min-w-0 overflow-y-auto">{children}</main>
    </div>
  );
}
