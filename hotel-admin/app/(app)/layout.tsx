"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Menu } from "lucide-react";
import { Nav, NAV_ITEMS, NAV_SECTION_OF, activeNavHref } from "@/components/nav";
import { ThemeToggle } from "@/components/theme-toggle";
import { ApplianceStatus } from "@/components/appliance-status";
import { Skeleton } from "@/components/ui/misc";
import { api, Whoami } from "@/lib/api";
import { useSidebarCollapsed } from "@/lib/sidebar-state";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const mainRef = useRef<HTMLElement | null>(null);
  const [me, setMe] = useState<Whoami | null>(null);
  const [loading, setLoading] = useState(true);
  const [drawer, setDrawer] = useState(false);
  // Desktop only. The drawer below `lg` is a full-width overlay and never consults this.
  const { collapsed, toggle: toggleCollapsed } = useSidebarCollapsed();

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

  // The drawer must close on navigation, or following a link on a phone leaves the menu covering the screen
  // that was just opened.
  useEffect(() => { setDrawer(false); }, [pathname]);

  // WHERE AM I. The top bar names the current screen and the group it belongs to, derived from the same table
  // the sidebar is built from so the two cannot disagree.
  const here = useMemo(() => {
    const href = activeNavHref(pathname);
    const item = NAV_ITEMS.find((i) => i.href === href);
    return { section: href ? NAV_SECTION_OF[href] : undefined, label: item?.label };
  }, [pathname]);

  async function onLogout() {
    try { await api.post("/auth/logout"); } catch {}
    router.replace("/login");
    router.refresh();
  }

  if (loading) {
    return (
      <div className="flex h-screen overflow-hidden">
        {/* The width token, not a literal: the pre-paint script has already decided this, so the
            placeholder and the real column agree and nothing moves when loading finishes. */}
        <div className="hidden w-[var(--sidebar-width)] shrink-0 border-r border-sidebar-border bg-sidebar lg:block" />
        <div className="flex-1 space-y-4 p-6">
          <Skeleton className="h-7 w-48" />
          <Skeleton className="h-4 w-72" />
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-24" />)}
          </div>
        </div>
      </div>
    );
  }
  if (!me) return null;

  const roles = me.roles ?? [];

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
    <div className="flex h-screen overflow-hidden">
      {/* Below `lg` the column becomes a drawer instead of disappearing. A 64px-wide icon rail was the other
          option and is worse here: these labels ("Duplicate sources", "Checkout grace") are not guessable
          from an icon. */}
      <div className="hidden lg:block">
        <Nav
          email={me.email}
          roles={roles}
          onLogout={onLogout}
          collapsed={collapsed}
          onToggleCollapsed={toggleCollapsed}
        />
      </div>

      <DialogPrimitive.Root open={drawer} onOpenChange={setDrawer}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-foreground/45 backdrop-blur-[2px] lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0" />
          <DialogPrimitive.Content
            className="fixed inset-y-0 left-0 z-50 h-full w-64 outline-none lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:slide-in-from-left data-[state=closed]:slide-out-to-left"
            aria-label="Navigation"
          >
            <DialogPrimitive.Title className="sr-only">Navigation</DialogPrimitive.Title>
            <Nav email={me.email} roles={roles} onLogout={onLogout} onNavigate={() => setDrawer(false)} />
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>

        <div className="flex min-w-0 flex-1 flex-col">
          {/*
            THE TOP BAR IS NEW, and it carries the three things that were previously nowhere: where you are,
            whether the appliance is healthy, and the theme control. Appliance health in particular was only
            visible by navigating to the dashboard or Diagnostics — so an operator on any other screen had no
            way to know the PMS had dropped or the database was unreachable.
          */}
          <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-background/85 px-4 backdrop-blur sm:px-6">
            <DialogPrimitive.Trigger
              className="-ml-1 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-surface hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 lg:hidden"
              aria-label="Open navigation"
            >
              <Menu className="size-5" />
            </DialogPrimitive.Trigger>

            <div className="min-w-0 flex-1">
              <nav aria-label="Breadcrumb" className="flex items-baseline gap-1.5">
                {here.section && (
                  <>
                    <span className="hidden truncate text-xs text-muted-foreground sm:inline">{here.section}</span>
                    <span className="hidden text-xs text-muted-foreground/50 sm:inline" aria-hidden>/</span>
                  </>
                )}
                <span className="truncate text-sm font-semibold">{here.label ?? "Hotel Admin"}</span>
              </nav>
            </div>

            <ApplianceStatus />
            <ThemeToggle />
          </header>

          {/*
            THE GUTTER LIVES HERE, so no screen can be built without one.
            --------------------------------------------------------------------------------------------------
            This is the fix for the most visible layout fault in the product. Roughly half the screens opened
            with `<div className="mx-auto w-full max-w-7xl space-y-5">` and the other half with `<div className="space-y-4">`,
            which has no padding at all — so those pages rendered flush against the window edge and the sidebar,
            with their cards touching both. The padded half did not agree either: `max-w-7xl`, `max-w-[92rem]`,
            `max-w-3xl` and `space-y-6 p-6` all appear.
            --------------------------------------------------------------------------------------------------
            Putting the padding and the measure on the scroll container means a page cannot get it wrong by
            omission. `PageShell` narrows the measure further where a screen wants it (a single form does not
            want 96rem) and owns the vertical rhythm; it no longer needs to supply the gutter.
          */}
          <main ref={mainRef} className="min-w-0 flex-1 overflow-y-auto">
            <div className="mx-auto w-full max-w-[104rem] px-4 py-5 sm:px-6 sm:py-6 lg:px-8">
              {children}
            </div>
          </main>
        </div>
      </DialogPrimitive.Root>
    </div>
  );
}
