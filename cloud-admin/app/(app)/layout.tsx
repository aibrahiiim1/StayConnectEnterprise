"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Menu } from "lucide-react";
import { Nav, NAV_ITEMS, NAV_SECTION_OF, activeNavHref } from "@/components/nav";
import { ThemeToggle } from "@/components/theme-toggle";
import { Skeleton } from "@/components/ui/misc";
import { ToastProvider } from "@/components/ui/toast";
import { StepUpProvider } from "@/components/step-up";
import { api, Whoami } from "@/lib/api";
import { CustomerProvider } from "@/lib/customer-context";
import { useSidebarCollapsed } from "@/lib/sidebar-state";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname() ?? "";
  const mainRef = useRef<HTMLElement | null>(null);
  const [me, setMe] = useState<Whoami | null>(null);
  const [loading, setLoading] = useState(true);
  const [drawer, setDrawer] = useState(false);
  const { collapsed, toggle: toggleCollapsed } = useSidebarCollapsed();

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

  // The content pane is the scrolling element, so reset it on navigation.
  useEffect(() => { mainRef.current?.scrollTo?.({ top: 0 }); }, [pathname]);
  // The drawer closes on navigation.
  useEffect(() => { setDrawer(false); }, [pathname]);

  const here = useMemo(() => {
    const href = activeNavHref(pathname);
    const item = NAV_ITEMS.find((i) => i.href === href);
    return { section: href ? NAV_SECTION_OF[href] : undefined, label: item?.label };
  }, [pathname]);

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
    <CustomerProvider me={me}>
      <ToastProvider>
        <StepUpProvider>
          <div className="flex h-screen overflow-hidden">
            <div className="hidden lg:block">
              <Nav email={me.email} onLogout={onLogout} collapsed={collapsed} onToggleCollapsed={toggleCollapsed} />
            </div>

            <DialogPrimitive.Root open={drawer} onOpenChange={setDrawer}>
              <DialogPrimitive.Portal>
                <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-foreground/45 backdrop-blur-[2px] lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0" />
                <DialogPrimitive.Content
                  className="fixed inset-y-0 start-0 z-50 h-full w-64 outline-none lg:hidden data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:slide-in-from-left data-[state=closed]:slide-out-to-left"
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
                <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-card/90 px-4 backdrop-blur sm:px-6">
                  <DialogPrimitive.Trigger
                    className="-ms-1 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-surface hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 lg:hidden"
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
                      <span className="truncate text-sm font-semibold" aria-current="page">{here.label ?? "Central"}</span>
                    </nav>
                  </div>

                  <ThemeToggle />
                </header>

                <main ref={mainRef} className="min-w-0 flex-1 overflow-y-auto">
                  <div className="mx-auto w-full max-w-[104rem] px-4 py-5 sm:px-6 sm:py-6 lg:px-8">{children}</div>
                </main>
              </div>
            </DialogPrimitive.Root>
          </div>
        </StepUpProvider>
      </ToastProvider>
    </CustomerProvider>
  );
}
