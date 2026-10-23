"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Menu } from "lucide-react";
import Link from "next/link";
import { Nav, crumbs } from "@/components/nav";
import { ThemeToggle } from "@/components/theme-toggle";
import { Skeleton } from "@/components/ui/misc";
import { ToastProvider } from "@/components/ui/toast";
import { StepUpProvider } from "@/components/step-up";
import { SessionExpiryWarning } from "@/components/session-expiry";
import { api, ApiError, BACKGROUND, setUnauthorizedHandler, type RequestOptions, type Whoami } from "@/lib/api";
import { deadlinesFrom, expiredSignInUrl, sameDeadlines, type Deadlines } from "@/lib/session-expiry";
import { SessionProvider } from "@/lib/session";
import { usePoll } from "@/lib/use-poll";
import { useSidebarCollapsed } from "@/lib/sidebar-state";
import { cn } from "@/lib/utils";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname() ?? "";
  const mainRef = useRef<HTMLElement | null>(null);
  const [me, setMe] = useState<Whoami | null>(null);
  const [loading, setLoading] = useState(true);
  const [drawer, setDrawer] = useState(false);
  const { collapsed, toggle: toggleCollapsed } = useSidebarCollapsed();

  const [deadlines, setDeadlines] = useState<Deadlines>({ idle: null, absolute: null });

  // Clear the stale cookie before redirecting so sign-in cannot bounce back into an expired session.
  const pathRef = useRef(pathname);
  pathRef.current = pathname;
  const ending = useRef(false);
  const expire = useCallback(() => {
    if (ending.current) return;
    ending.current = true;
    const here = pathRef.current + (typeof window !== "undefined" ? window.location.search : "");
    void api.post("/v1/auth/logout").catch(() => undefined).finally(() => router.replace(expiredSignInUrl(here)));
  }, [router]);
  useEffect(() => setUnauthorizedHandler(() => expire()), [expire]);

  // Keep deadline refreshes separate from identity to avoid rerendering every screen.
  const take = useCallback((m: Whoami): Deadlines => {
    const { idle_expires_at, absolute_expires_at, server_time, ...who } = m;
    const d = deadlinesFrom({ idle_expires_at, absolute_expires_at, server_time });
    setMe((prev) => (prev && JSON.stringify(prev) === JSON.stringify(who) ? prev : who));
    setDeadlines((prev) => (sameDeadlines(prev, d) ? prev : d));
    return d;
  }, []);
  const readWhoami = useCallback((opts?: RequestOptions) => api.get<Whoami>("/v1/auth/whoami", opts).then(take), [take]);

  useEffect(() => {
    let cancelled = false;
    api.get<Whoami>("/v1/auth/whoami")
      .then((m) => { if (!cancelled) take(m); })
      .catch((e) => {
        if (!cancelled && !(e instanceof ApiError && e.status === 401)) router.replace("/login");
      })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [router, take]);

  // Visible-tab polling applies role changes promptly without extending the idle session.
  usePoll((opts) => { readWhoami(opts).catch(() => undefined); }, 30000, !loading);
  useEffect(() => { mainRef.current?.scrollTo?.({ top: 0 }); }, [pathname]);
  useEffect(() => { setDrawer(false); }, [pathname]);

  const here = useMemo(() => crumbs(pathname), [pathname]);
  useEffect(() => { document.title = `${here.label} · OneGate Central`; }, [here.label]);

  async function onLogout() {
    try { await api.post("/v1/auth/logout"); } catch {}
    router.replace("/login");
    router.refresh();
  }

  if (loading) {
    return (
      <div className="flex h-screen overflow-hidden" aria-busy="true">
        <div className="hidden w-[var(--sidebar-width)] shrink-0 border-e border-sidebar-border bg-sidebar lg:block" />
        <div className="flex-1 space-y-4 p-6">
          <span className="sr-only">Loading</span>
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

  return (
    <SessionProvider me={me}>
      <ToastProvider>
        <StepUpProvider>
          <SessionExpiryWarning
            deadlines={deadlines}
            refresh={() => readWhoami(BACKGROUND)}
            keepAlive={() => api.get("/v1/auth/mfa")}
            onExpired={expire}
            onSignOut={onLogout}
          />
          <div className="flex h-screen overflow-hidden">
            {/* The first tab stop skips the sidebar for keyboard users. */}
            <a
              href="#main"
              className={cn(
                "sr-only focus:not-sr-only focus:fixed focus:start-3 focus:top-3 focus:z-[70] focus:rounded-md",
                "focus:bg-card focus:px-4 focus:py-2.5 focus:text-sm focus:font-semibold focus:text-foreground focus:shadow-overlay",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              )}
            >
              Skip to content
            </a>
            <div className="hidden lg:block">
              <Nav email={me.email} onLogout={onLogout} collapsed={collapsed} onToggleCollapsed={toggleCollapsed} />
            </div>

            <DialogPrimitive.Root open={drawer} onOpenChange={setDrawer}>
              <DialogPrimitive.Portal>
                <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-scrim/45 backdrop-blur-[2px] lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 motion-reduce:animate-none" />
                <DialogPrimitive.Content
                  className="fixed inset-y-0 start-0 z-50 h-full w-64 outline-none lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:slide-in-from-left data-[state=closed]:slide-out-to-left motion-reduce:animate-none"
                  aria-label="Navigation"
                  aria-describedby={undefined}
                  onOpenAutoFocus={(e) => {
                    e.preventDefault();
                    (e.currentTarget as HTMLElement | null)?.focus({ preventScroll: true });
                  }}
                >
                  <DialogPrimitive.Title className="sr-only">Navigation</DialogPrimitive.Title>
                  <Nav email={me.email} onLogout={onLogout} onNavigate={() => setDrawer(false)} />
                </DialogPrimitive.Content>
              </DialogPrimitive.Portal>

              <div className="flex min-w-0 flex-1 flex-col">
                <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-card px-4 sm:px-6">
                  <DialogPrimitive.Trigger
                    className="-ms-1 inline-flex items-center justify-center rounded-md p-1.5 text-muted-foreground transition-colors pointer-coarse:size-11 hover:bg-surface hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 lg:hidden"
                    aria-label="Open navigation"
                  >
                    <Menu className="size-5" />
                  </DialogPrimitive.Trigger>

                  <div className="min-w-0 flex-1">
                    <nav aria-label="Breadcrumb" className="flex items-baseline gap-1.5">
                      {here.parent && (
                        <>
                          <Link
                            href={here.parent.href}
                            className="hidden truncate rounded text-xs text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:inline"
                          >
                            {here.parent.label}
                          </Link>
                          <span className="hidden text-xs text-muted-foreground/50 sm:inline" aria-hidden>/</span>
                        </>
                      )}
                      <span className="truncate text-sm font-semibold" aria-current="page">{here.label}</span>
                    </nav>
                  </div>

                  <ThemeToggle />
                </header>

                <main id="main" tabIndex={-1} ref={mainRef} className="min-w-0 flex-1 overflow-y-auto focus:outline-none">
                  <div className="mx-auto w-full max-w-[104rem] px-4 py-5 sm:px-6 sm:py-6 lg:px-8">{children}</div>
                </main>
              </div>
            </DialogPrimitive.Root>
          </div>
        </StepUpProvider>
      </ToastProvider>
    </SessionProvider>
  );
}
