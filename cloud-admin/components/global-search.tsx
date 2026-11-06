"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { BadgeCheck, Building2, MapPin, Search, Server, StickyNote } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/misc";
import { usePermissions } from "@/lib/permissions";
import { cn } from "@/lib/utils";

type Hit = { id: string; title: string; subtitle: string; href: string; matched?: string };
type Result = { query: string; customers: Hit[]; sites: Hit[]; appliances: Hit[]; licenses: Hit[]; notes: Hit[] };

const GROUPS: { key: keyof Omit<Result, "query">; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
  { key: "appliances", label: "Appliances", icon: Server },
  { key: "customers", label: "Customers", icon: Building2 },
  { key: "sites", label: "Sites", icon: MapPin },
  { key: "licenses", label: "Licenses", icon: BadgeCheck },
  { key: "notes", label: "Notes", icon: StickyNote },
];

function typingIn(el: EventTarget | null): boolean {
  const t = el as HTMLElement | null;
  return !!t && (t.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName));
}

export function GlobalSearch() {
  const { can } = usePermissions();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [res, setRes] = useState<Result | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [active, setActive] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.key === "k" || e.key === "K") && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        setOpen(true);
      } else if (e.key === "/" && !typingIn(e.target) && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        setOpen(true);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (!open) {
      setQ("");
      setRes(null);
      setErr(null);
      setBusy(false);
    }
  }, [open]);

  useEffect(() => {
    const term = q.trim();
    setActive(0);
    if (term.length < 2) {
      setRes(null);
      setErr(null);
      setBusy(false);
      return;
    }
    let live = true;
    setBusy(true);
    const t = window.setTimeout(() => {
      api.get<Result>(`/cloud/v1/search?q=${encodeURIComponent(term)}`)
        .then((r) => { if (live) { setRes(r); setErr(null); } })
        .catch((e) => { if (live) setErr(e instanceof ApiError ? e.message : "The search could not run. Try again."); })
        .finally(() => { if (live) setBusy(false); });
    }, 200);
    return () => { live = false; window.clearTimeout(t); };
  }, [q]);

  const flat = useMemo(() => {
    if (!res) return [] as (Hit & { group: string })[];
    return GROUPS.flatMap((g) => res[g.key].map((h) => ({ ...h, group: g.key })));
  }, [res]);

  const go = useCallback((h: Hit) => {
    setOpen(false);
    router.push(h.href);
  }, [router]);

  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  if (!can["customers.list"]) return null;

  function onKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (!flat.length) return;
    if (e.key === "ArrowDown") { e.preventDefault(); setActive((i) => (i + 1) % flat.length); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setActive((i) => (i - 1 + flat.length) % flat.length); }
    else if (e.key === "Enter") { e.preventDefault(); go(flat[active]); }
  }

  const term = q.trim();
  let index = -1;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className={cn(
          "inline-flex h-9 items-center gap-2 rounded-md border border-border bg-surface px-2.5 text-sm text-muted-foreground",
          "transition-colors hover:border-border-strong hover:text-foreground pointer-coarse:h-11",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        )}
        aria-label="Search Central"
        aria-keyshortcuts="Control+K"
      >
        <Search className="size-4" aria-hidden />
        <span className="hidden sm:inline">Search</span>
        <kbd className="hidden rounded border border-border px-1.5 font-sans text-2xs md:inline">Ctrl K</kbd>
      </button>
      <DialogContent size="lg" className="overflow-hidden p-0" aria-describedby={undefined}>
        <DialogTitle className="sr-only">Search Central</DialogTitle>
        <div className="flex items-center gap-2 border-b border-border px-4 pe-12">
          <Search className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={onKeyDown}
            maxLength={100}
            placeholder="Serial, customer, site, licence, tag or words in a note"
            aria-label="Search"
            role="combobox"
            aria-expanded={flat.length > 0}
            aria-controls="global-search-results"
            aria-activedescendant={flat.length ? `gs-${active}` : undefined}
            className="h-12 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          />
          {busy && <Spinner className="size-4" label="Searching" />}
        </div>
        <div ref={listRef} id="global-search-results" role="listbox" aria-label="Results" className="max-h-[60vh] overflow-y-auto p-2">
          {term.length < 2 && (
            <p className="px-2 py-6 text-center text-sm text-muted-foreground">
              Type at least 2 characters. Press <kbd className="font-sans">/</kbd> or Ctrl K anywhere to search.
            </p>
          )}
          {err && <p role="alert" className="px-2 py-6 text-center text-sm text-destructive">{err}</p>}
          {res && !err && flat.length === 0 && !busy && (
            <p className="px-2 py-6 text-center text-sm text-muted-foreground">Nothing found for &ldquo;{res.query}&rdquo;.</p>
          )}
          {res && !err && GROUPS.map((g) => {
            const hits = res[g.key];
            if (!hits.length) return null;
            const Icon = g.icon;
            return (
              <div key={g.key} role="group" aria-label={g.label} className="py-1">
                <div className="px-2 pb-1 pt-2 text-2xs font-medium uppercase tracking-wide text-muted-foreground">{g.label}</div>
                {hits.map((h) => {
                  index += 1;
                  const i = index;
                  return (
                    <div
                      key={h.id}
                      id={`gs-${i}`}
                      data-index={i}
                      role="option"
                      aria-selected={i === active}
                      onMouseMove={() => setActive(i)}
                      onClick={() => go(h)}
                      className={cn(
                        "flex cursor-pointer items-start gap-3 rounded-md px-2 py-2",
                        i === active ? "bg-accent text-foreground" : "text-foreground",
                      )}
                    >
                      <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium">{h.title}</div>
                        {h.subtitle && <div className="truncate text-xs text-muted-foreground">{h.subtitle}</div>}
                      </div>
                      {h.matched && <span className="shrink-0 text-2xs text-muted-foreground">{h.matched}</span>}
                    </div>
                  );
                })}
              </div>
            );
          })}
        </div>
      </DialogContent>
    </Dialog>
  );
}
