"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Menu } from "lucide-react";
import { Nav, NAV_ITEMS, NAV_SECTION_OF, activeNavHref } from "@/components/nav";
import { ThemeToggle } from "@/components/theme-toggle";
import { ApplianceStatus } from "@/components/appliance-status";
import { Skeleton } from "@/components/ui/misc";
import { api, type Whoami } from "@/lib/api";
import { loginAfterExpiry } from "@/lib/safe-next";
import { IdleWarning } from "@/components/idle-warning";
import { useSidebarCollapsed } from "@/lib/sidebar-state";
import { useCapabilities, surfaceAvailable } from "@/lib/capabilities";
import { SurfaceNotEnabled } from "@/components/surface-not-enabled";
import { NotPermitted } from "@/components/not-permitted";
import { canRead } from "@/lib/roles";
import { ToastProvider } from "@/components/ui/toast";
import { WhoamiProvider, useWhoamiSession } from "@/lib/whoami-context";
import { ForcedPasswordChange } from "@/components/forced-password-change";

// Screens reached by link, not the menu; listed here so a role that cannot read them sees "not permitted"
// instead of a shell of 403 banners. Every other off-menu route is a server redirect.
const OFF_MENU_SCREENS: { href: string; resource: string; label: string }[] = [
  { href: "/pms-reconciliation", resource: "pms-reconciliation", label: "Unresolved departures" },
  { href: "/roster-reconciliation", resource: "pms-roster-reconciliation", label: "Roster reconciliation" },
];

const APP_TITLE = "OneGate Admin Console";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const mainRef = useRef<HTMLElement | null>(null);
  const [drawer, setDrawer] = useState(false);
  // Desktop only; the drawer below `lg` never consults this.
  const { collapsed, toggle: toggleCollapsed } = useSidebarCollapsed();
  // A page whose resource is not mounted renders an explanation instead of itself.
  const caps = useCapabilities();
  const unavailable = useMemo(() => {
    const href = activeNavHref(pathname);
    const item = NAV_ITEMS.find((i) => i.href === href);
    if (!item || surfaceAvailable(caps, item.capability ?? item.resource)) return null;
    return item.label;
  }, [pathname, caps]);

  // Children wait for the capability answer: until it arrives `unavailable` is null, so a page that will be
  // replaced by the explanation would otherwise mount and fire a doomed request. loadCapabilities caches, and
  // a failure resolves to an empty set (fails open).
  const capsKnown = caps !== null;

  // The session cookie is stale (expired, or edged restarted). Clear it explicitly so the middleware does not
  // bounce /login back to /dashboard (a redirect loop); lib/safe-next.ts decides whether to return afterwards.
  const bounce = useCallback(async () => {
    try { await api.post("/auth/logout"); } catch {}
    router.replace(loginAfterExpiry(window.location.pathname + window.location.search));
  }, [router]);

  // Re-validated every 30 seconds so a role change reaches the nav and gated pages without a reload.
  const { me, loading } = useWhoamiSession(bounce);

  // The content pane scrolls, not the window, so Next's scroll-to-top has nothing to reset.
  useEffect(() => { mainRef.current?.scrollTo({ top: 0 }); }, [pathname]);

  useEffect(() => { setDrawer(false); }, [pathname]);

  // Derived from the sidebar's own table so the top bar and the menu cannot disagree.
  const here = useMemo(() => {
    const href = activeNavHref(pathname);
    const item = NAV_ITEMS.find((i) => i.href === href);
    return { section: href ? NAV_SECTION_OF[href] : undefined, label: item?.label };
  }, [pathname]);

  // The tab title follows the top bar's label (WCAG 2.4.2); off-menu screens name themselves from their table.
  const pageLabel = here.label
    ?? OFF_MENU_SCREENS.find((i) => pathname === i.href || pathname.startsWith(i.href + "/"))?.label;
  useEffect(() => {
    const wanted = pageLabel ? `${pageLabel} · ${APP_TITLE}` : APP_TITLE;
    const apply = () => { if (document.title !== wanted) document.title = wanted; };
    apply();
    // Next streams the root metadata <title> after hydration, which would otherwise overwrite this one.
    const observer = new MutationObserver(apply);
    observer.observe(document.head, { childList: true, subtree: true, characterData: true });
    return () => observer.disconnect();
  }, [pageLabel]);

  // A request edged counts as activity and answers the new session end.
  const keepAlive = useCallback(
    () => api.get<{ expires_at: string }>("/auth/keepalive").then((r) => r.expires_at),
    [],
  );
  const recheck = useCallback(
    () => api.get<Whoami>("/auth/whoami").then((m) => m.expires_at),
    [],
  );

  async function onLogout() {
    try { await api.post("/auth/logout"); } catch {}
    router.replace("/login");
    router.refresh();
  }

  if (loading) {
    return (
      <div className="flex h-screen overflow-hidden">
        {/* The width token, so the placeholder matches the real column. */}
        <div className="hidden w-[var(--sidebar-width)] shrink-0 border-e border-sidebar-border bg-sidebar lg:block" />
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

  // edged refuses every other call for this session, so the shell would only render refusals.
  if (me.must_change_password) {
    return (
      <ForcedPasswordChange email={me.email} onLogout={onLogout} onChanged={() => window.location.reload()} />
    );
  }

  const roles = me.roles ?? [];
  // The menu hides screens a role cannot read, but a URL still reaches them; the same table decides here and
  // the page does not mount, so it sends nothing that would be refused.
  const activeItem = NAV_ITEMS.find((i) => i.href === activeNavHref(pathname))
    ?? OFF_MENU_SCREENS.find((i) => pathname === i.href || pathname.startsWith(i.href + "/"));
  const notPermitted = activeItem && !canRead(activeItem.resource, roles) ? activeItem.label : null;

  return (
    // h-screen + overflow-hidden (not min-h-screen) bounds the container so the sidebar scrolls on its own and
    // keeps its position across navigation, while the content scrolls in its own pane.
    <WhoamiProvider value={me}>
    <ToastProvider>
    {/* Skip link (WCAG 2.4.1); focus is moved by hand because a hash link alone is unreliable across browsers. */}
    <a
      href="#content"
      onClick={(e) => {
        e.preventDefault();
        mainRef.current?.focus();
      }}
      className="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-[100] focus:rounded-md focus:bg-card focus:px-4 focus:py-2 focus:text-sm focus:font-semibold focus:text-foreground focus:shadow-overlay focus:outline-none focus:ring-2 focus:ring-ring"
    >
      Skip to content
    </a>
    <IdleWarning
      expiresAt={me.expires_at}
      onStay={keepAlive}
      onCheck={recheck}
      onSignOut={() => void onLogout()}
      onExpired={() => void bounce()}
    />
    <div className="flex h-screen overflow-hidden">
      {/* Below `lg` the column becomes a drawer; an icon rail would not work because labels are not guessable from icons. */}
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
          <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-scrim/45 backdrop-blur-[2px] lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 motion-reduce:animate-none" />
          <DialogPrimitive.Content
            className="fixed inset-y-0 start-0 z-50 h-full w-64 outline-none lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:slide-in-from-left data-[state=closed]:slide-out-to-left motion-reduce:animate-none"
            aria-label="Navigation"
            // Radix would focus the nav filter and raise the phone keyboard over the menu; focus the panel instead.
            onOpenAutoFocus={(e) => {
              e.preventDefault();
              (e.currentTarget as HTMLElement | null)?.focus({ preventScroll: true });
            }}
          >
            <DialogPrimitive.Title className="sr-only">Navigation</DialogPrimitive.Title>
            <Nav email={me.email} roles={roles} onLogout={onLogout} onNavigate={() => setDrawer(false)} />
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>

        <div className="flex min-w-0 flex-1 flex-col">
          {/* Solid, not translucent: content scrolls in its own pane, so nothing passes under this bar. */}
          <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-card px-4 sm:px-6">
            <DialogPrimitive.Trigger
              className="-ms-1.5 inline-flex size-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring pointer-coarse:size-11 lg:hidden"
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
                <span className="truncate text-sm font-semibold">{here.label ?? "Admin Console"}</span>
              </nav>
            </div>

            <ApplianceStatus />
            <ThemeToggle />
          </header>

          {/* The gutter and measure live here so no page can omit them; PageShell narrows the measure and owns
              vertical rhythm. This is the skip link's target (tabIndex -1: focusable, no tab stop). */}
          <main ref={mainRef} id="content" tabIndex={-1} className="min-w-0 flex-1 overflow-y-auto focus:outline-none">
            <div className="mx-auto w-full max-w-[104rem] px-4 py-5 sm:px-6 sm:py-6 lg:px-8">
              {!capsKnown ? <Skeleton className="h-64" />
                : unavailable ? <SurfaceNotEnabled label={unavailable} />
                : notPermitted ? <NotPermitted label={notPermitted} />
                : children}
            </div>
          </main>
        </div>
      </DialogPrimitive.Root>
    </div>
    </ToastProvider>
    </WhoamiProvider>
  );
}
