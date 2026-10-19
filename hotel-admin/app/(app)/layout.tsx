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

// SCREENS REACHED BY LINK, NOT FROM THE MENU. They are opened from other pages (PMS connection, Stays), so the
// menu's table does not list them; without this a role that cannot read them saw a shell of 403 banners instead
// of the "not permitted" page (found in PRE-LIVE acceptance, 2026-10-16). Every other route outside the menu is a
// server redirect.
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
  // Desktop only. The drawer below `lg` is a full-width overlay and never consults this.
  const { collapsed, toggle: toggleCollapsed } = useSidebarCollapsed();
  // WHETHER THIS APPLIANCE CAN SERVE THE SCREEN THE OPERATOR IS ON.
  //
  // Handled here rather than in each page because it is the same answer for all of them, and because the
  // eight that needed it were eight chances to write a slightly different wrong one. A page whose resource
  // is not mounted renders an explanation instead of itself -- reached by menu, by bookmark or by typed URL.
  const caps = useCapabilities();
  const unavailable = useMemo(() => {
    const href = activeNavHref(pathname);
    const item = NAV_ITEMS.find((i) => i.href === href);
    if (!item || surfaceAvailable(caps, item.capability ?? item.resource)) return null;
    return item.label;
  }, [pathname, caps]);

  // A PAGE THAT IS ABOUT TO BE REPLACED MUST NOT LOAD FIRST.
  //
  // The final sweep caught this with one page left: Cross-PMS transfer still logged a 404. Until the
  // capability answer arrives, `unavailable` is null -- it has to be, or every screen would flash an
  // explanation on every load -- so the real page mounted, fired the request its data needs, and was
  // replaced a moment later by the explanation. The operator saw the right thing; the appliance still got a
  // doomed request and the console still got an error, which is exactly the noise this delivery removed
  // everywhere else.
  //
  // So children wait for the answer rather than racing it. The cost is one request on the first navigation
  // of a session: loadCapabilities caches for the lifetime of the page, so every subsequent screen renders
  // immediately. Nothing waits on a failure either -- the loader resolves to an empty set, which fails open.
  const capsKnown = caps !== null;

  // Session cookie is stale/invalid (expired, or edged restarted and dropped
  // its in-memory sessions). Explicitly clear the cookie so the middleware
  // won't bounce /login back to /dashboard (a redirect loop), then show the
  // login form. The login page is told why and where the operator was, so it can say the session ended and
  // bring them back afterwards (lib/safe-next.ts decides whether that place may be followed).
  const bounce = useCallback(async () => {
    try { await api.post("/auth/logout"); } catch {}
    router.replace(loginAfterExpiry(window.location.pathname + window.location.search));
  }, [router]);

  // Who is signed in: asked once, then re-validated every 30 seconds (see useWhoamiSession). A failure bounces
  // to /login; a success replaces the identity, so a role granted or revoked meanwhile reaches the nav and
  // every role-gated page without a reload.
  const { me, loading } = useWhoamiSession(bounce);

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

  // EVERY SCREEN HAD THE SAME TAB TITLE (WCAG 2.4.2), so a tab list, the history menu and a screen reader's
  // window announcement all said "OneGate Admin Console" whatever was open. The title follows the same label the
  // top bar shows; a screen reached by link names itself from its own table.
  const pageLabel = here.label
    ?? OFF_MENU_SCREENS.find((i) => pathname === i.href || pathname.startsWith(i.href + "/"))?.label;
  useEffect(() => {
    document.title = pageLabel ? `${pageLabel} · ${APP_TITLE}` : APP_TITLE;
  }, [pageLabel]);

  // "Stay signed in" on the idle warning: a request edged counts as activity, answering the new end.
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

  // A SESSION THAT MUST REPLACE ITS PASSWORD SEES NOTHING ELSE. edged refuses every other call for it, so the
  // shell would only render a screen of refusals. Once the password is changed the page reloads into the console.
  if (me.must_change_password) {
    return (
      <ForcedPasswordChange email={me.email} onLogout={onLogout} onChanged={() => window.location.reload()} />
    );
  }

  const roles = me.roles ?? [];
  // WHETHER THE OPERATOR'S ROLE MAY READ THE SCREEN THEY ARE ON (audit L-2). The menu hides it; a URL or a
  // bookmark still reaches it, and the page used to open as a shell of 403 banners. The same table the menu
  // uses decides, and the page does not mount, so it sends nothing that would be refused.
  const activeItem = NAV_ITEMS.find((i) => i.href === activeNavHref(pathname))
    ?? OFF_MENU_SCREENS.find((i) => pathname === i.href || pathname.startsWith(i.href + "/"));
  const notPermitted = activeItem && !canRead(activeItem.resource, roles) ? activeItem.label : null;

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
    <WhoamiProvider value={me}>
    <ToastProvider>
    {/* SKIP TO CONTENT (WCAG 2.4.1): the first stop on every screen, visible only when focused, so a keyboard
        user does not tab through thirty-odd menu links to reach the page they opened. Focus is moved by hand
        because a hash link alone does not move it reliably in every browser. */}
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
      {/* Below `lg` the column becomes a drawer instead of disappearing. A 64px-wide icon rail was the other
          option and is worse here: these labels ("Duplicate sources", "Grace Period") are not guessable
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
          <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-scrim/45 backdrop-blur-[2px] lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 motion-reduce:animate-none" />
          <DialogPrimitive.Content
            className="fixed inset-y-0 left-0 z-50 h-full w-64 outline-none lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:slide-in-from-left data-[state=closed]:slide-out-to-left motion-reduce:animate-none"
            aria-label="Navigation"
            /*
              OPENING A MENU IS NOT ASKING TO TYPE.

              Radix focuses a dialog's first focusable element on open. In this drawer that is the navigation
              filter, so tapping the hamburger on a phone raised the software keyboard over the menu the
              operator had just asked to see — they then had to dismiss a keyboard before they could read
              anything. Focus goes to the panel itself instead: Escape still closes it, Tab still walks it,
              and the filter is still one tap away when somebody actually wants it.

              Desktop is untouched. There is no drawer above `lg`, and Ctrl+K focuses the filter as before.
            */
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
          {/*
            THE TOP BAR IS NEW, and it carries the three things that were previously nowhere: where you are,
            whether the appliance is healthy, and the theme control. Appliance health in particular was only
            visible by navigating to the dashboard or Diagnostics — so an operator on any other screen had no
            way to know the PMS had dropped or the database was unreachable.
          */}
          {/* Solid, not translucent: the content scrolls in its own pane below, so nothing ever passes under
              this bar for a blur to show. */}
          <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-card px-4 sm:px-6">
            <DialogPrimitive.Trigger
              className="-ml-1.5 inline-flex size-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-surface hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 pointer-coarse:size-11 lg:hidden"
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
          {/* The skip link's target. tabIndex -1 lets it take focus without adding a tab stop. */}
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
