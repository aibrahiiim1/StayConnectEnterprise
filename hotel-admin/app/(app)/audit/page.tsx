"use client";

// ACTIVITY — who changed what, and when.
//
// WHAT THIS REPLACES. A raw table of the audit rows: a timestamp, an actor uuid, a dotted action code, a
// target uuid, an IP and a payload column that said "—". Every fact was present and none of it was legible.
//
// The trail itself is unchanged and untouchable: append-only, nothing rewritten, nothing backfilled. What
// changed is that the screen now reads it out loud. Each row leads with a sentence, a person and a time; the
// exact action code, the ids, the address and the payload are one click away, because the day this screen
// matters most is the day somebody needs the precise record rather than the readable one.
//
// SEVERITY IS NOT INVENTED. Three levels, assigned per action in lib/audit-words.ts: a notice (something
// happened), a change (configuration moved) and a security event (someone saw a guest's typed credentials,
// a backup left the appliance, an unlicensed-mode attempt was refused). Anything unrecognised is shown with
// its code intact rather than guessed at.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { PageShell, PageHeader, Toolbar } from "@/components/ui/page";
import { Card, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { FilterChips, KeyValueGrid, SearchInput } from "@/components/ui/data";
import { refreshingClass } from "@/components/ui/patterns";
import { cn, formatDate } from "@/lib/utils";
import {
  auditWords, auditActor, AUDIT_CATEGORIES, type AuditCategory,
} from "@/lib/audit-words";
import { ScrollText, ChevronRight, ShieldAlert, RefreshCw } from "lucide-react";

type Row = {
  ts: string;
  actor_type?: string | null;
  actor_id?: string | null;
  action: string;
  target_type?: string | null;
  target_id?: string | null;
  ip?: string | null;
  payload?: unknown;
};

/** The prefix that selects a category server-side. One clause instead of enumerating every code. */
const CATEGORY_PREFIX: Record<AuditCategory, string[]> = {
  "Sign-in & access": ["operator", "session", "guest_signin", "auth_methods"],
  "Guest portal": ["branding", "portal_asset"],
  "Internet offering": ["commercial_package", "service_plan", "checkout_grace"],
  "Property management system": ["pms_"],
  "Networks": ["network"],
  "Licence & cloud": ["license", "cloud", "renewal", "hotel_admin_cert"],
  "Backups": ["backup"],
  "Diagnostics": ["health"],
};

const LIMIT = 500;

const RANGES = [
  { label: "Last 24 hours", hours: 24 },
  { label: "Last 7 days", hours: 24 * 7 },
  { label: "Last 30 days", hours: 24 * 30 },
  { label: "Everything", hours: 0 },
];

type Chip = "all" | "security" | AuditCategory;

export default function ActivityPage() {
  const [rows, setRows] = useState<Row[] | null>(null);
  // Whether the rows on screen were narrowed by the server to one category. Counts per category are only
  // honest when they were not: a narrowed page has nothing to say about the other categories.
  const [narrowed, setNarrowed] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [range, setRange] = useState(RANGES[2]);
  const [chip, setChip] = useState<Chip>("all");
  const [text, setText] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [names, setNames] = useState<Map<string, string>>(new Map());

  const category: AuditCategory | "" = chip === "all" || chip === "security" ? "" : chip;
  const securityOnly = chip === "security";

  const load = useCallback(async () => {
    setBusy(true); setErr(null);
    try {
      const p = new URLSearchParams({ limit: String(LIMIT) });
      if (range.hours) p.set("from", new Date(Date.now() - range.hours * 3600_000).toISOString());
      // A category becomes one prefix clause. Several prefixes per category means several requests would be
      // wrong; instead the widest prefix is sent and the rest is narrowed below, on a page the server sized.
      let serverNarrowed = false;
      if (category) {
        const prefixes = CATEGORY_PREFIX[category];
        if (prefixes.length === 1) { p.set("action_prefix", prefixes[0]); serverNarrowed = true; }
      }
      const r = await api.get<{ data: Row[] }>(`/audit?${p.toString()}`);
      setRows(r.data ?? []);
      setNarrowed(serverNarrowed);
    } catch (e) {
      // The last answer stays on screen; the banner says this one failed.
      setErr(e instanceof ApiError ? e.message : "the activity trail could not be read");
    } finally { setBusy(false); }
  }, [range, category]);

  useEffect(() => { load(); }, [load]);

  // THE OPERATOR DIRECTORY, ONCE. An audit row records who by id, which is the precise fact and the useless
  // one -- nobody scanning for "who changed the retention policy" can read a uuid. The exact id stays in the
  // expandable record; this is what puts a name on the summary line. Failure is silent on purpose: an
  // activity trail that would not render because a second request failed is worse than one showing ids.
  useEffect(() => {
    api.get<{ data: { id: string; display_name?: string; email?: string }[] }>("/operators")
      .then((r) => setNames(new Map((r.data ?? []).map((o) => [o.id, o.display_name || o.email || o.id]))))
      .catch(() => {});
  }, []);

  const visible = useMemo(() => {
    if (!rows) return null;
    const q = text.trim().toLowerCase();
    return rows.filter((r) => {
      const w = auditWords(r.action);
      if (category && w.category !== category) return false;
      if (securityOnly && w.severity !== "security") return false;
      if (!q) return true;
      return (
        w.title.toLowerCase().includes(q) ||
        r.action.toLowerCase().includes(q) ||
        (r.actor_id ?? "").toLowerCase().includes(q) ||
        (r.target_id ?? "").toLowerCase().includes(q) ||
        (r.ip ?? "").toLowerCase().includes(q)
      );
    });
  }, [rows, text, category, securityOnly]);

  const counts = useMemo(() => {
    const out = { security: 0, byCategory: new Map<AuditCategory, number>() };
    for (const r of rows ?? []) {
      const w = auditWords(r.action);
      if (w.severity === "security") out.security++;
      out.byCategory.set(w.category, (out.byCategory.get(w.category) ?? 0) + 1);
    }
    return out;
  }, [rows]);

  const chipOptions: { value: Chip; label: React.ReactNode; count?: number; tone?: "warn" }[] = [
    { value: "all", label: "Everything", count: narrowed || !rows ? undefined : rows.length },
    // SECURITY EVENTS FIRST, because that is the filter somebody reaches for under pressure.
    {
      value: "security",
      label: <><ShieldAlert className="size-3.5" aria-hidden /> Security</>,
      count: narrowed || !rows ? undefined : counts.security,
      tone: "warn",
    },
    ...AUDIT_CATEGORIES.map((c) => ({
      value: c as Chip,
      label: c,
      count: narrowed || !rows ? undefined : counts.byCategory.get(c) ?? 0,
    })),
  ];

  const filtered = chip !== "all" || text.trim() !== "";
  const clearFilters = () => { setChip("all"); setText(""); };

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<ScrollText />}
        eyebrow="System"
        title="Activity"
        description="Every change made to this appliance, by staff and by the system itself. The record is written as it happens and is never edited or removed."
        actions={
          <Button variant="secondary" onClick={load} disabled={busy}>
            <RefreshCw className={cn(busy && "animate-spin motion-reduce:animate-none")} />
            {busy ? "Reading…" : "Refresh"}
          </Button>
        }
      />

      <ErrorBanner err={err} className="mb-0" />

      <div className="space-y-3">
        <Toolbar className="items-center">
          <SearchInput
            value={text}
            onChange={setText}
            label="Search the activity trail"
            placeholder="Search — what happened, who, an address, an id"
            className="sm:w-96"
          />
          <Select
            aria-label="Period"
            className="h-9 w-full sm:w-44"
            value={range.label}
            onChange={(e) => setRange(RANGES.find((x) => x.label === e.target.value) ?? RANGES[2])}
          >
            {RANGES.map((r) => <option key={r.label}>{r.label}</option>)}
          </Select>
        </Toolbar>
        <FilterChips<Chip> label="Show" value={chip} onChange={setChip} options={chipOptions} />
      </div>

      <Card className="overflow-hidden">
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle>
              {visible === null ? "Activity" : `${visible.length.toLocaleString()} ${visible.length === 1 ? "entry" : "entries"}`}
            </CardTitle>
            <CardDescription>{range.label} · up to {LIMIT} entries per request</CardDescription>
          </div>
        </CardHeader>

        {rows && rows.length >= LIMIT && (
          <Callout tone="info" className="m-4 mb-0">
            Only the first {LIMIT} entries for this period are shown. Choose a shorter period or a category to see
            the rest.
          </Callout>
        )}

        <div className={cn(busy && rows && refreshingClass)}>
          {visible === null ? (
            err ? (
              <EmptyState icon={<ScrollText />} title="The activity trail could not be read"
                hint="It appears here once the appliance answers." />
            ) : <SkeletonRows rows={6} cols={3} />
          ) : visible.length === 0 ? (
            filtered ? (
              <EmptyState icon={<ScrollText />} title="Nothing matches"
                hint="No recorded activity fits these filters. Widen the period or clear the search."
                action={<Button variant="secondary" size="sm" onClick={clearFilters}>Clear filters</Button>} />
            ) : (
              <EmptyState icon={<ScrollText />} title="No activity in this period"
                hint="Nothing was recorded. Choose a longer period to look further back." />
            )
          ) : (
            <ul className="divide-y divide-border">
              {visible.map((r, i) => {
                const w = auditWords(r.action);
                const id = `${r.ts}-${i}`;
                const isOpen = open === id;
                const panel = `activity-${i}`;
                return (
                  <li key={id}>
                    <button
                      type="button"
                      className="flex w-full items-start gap-3 px-4 py-3 text-start transition-colors hover:bg-accent/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring sm:px-5"
                      aria-expanded={isOpen}
                      aria-controls={panel}
                      onClick={() => setOpen(isOpen ? null : id)}
                    >
                      <ChevronRight
                        className={cn(
                          "mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform rtl:-scale-x-100",
                          isOpen && "rotate-90 rtl:rotate-90",
                        )}
                        aria-hidden
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="text-emphasis">{w.title}</span>
                          {w.severity === "security" && (
                            <Badge tone="warn"><ShieldAlert className="size-3" aria-hidden /> Security</Badge>
                          )}
                          <Badge tone="default">{w.category}</Badge>
                        </div>
                        <div className="mt-0.5 text-caption text-muted-foreground">
                          {auditActor(r.actor_type, r.actor_id, names)} · {formatDate(r.ts)}
                          {r.ip ? ` · from ${r.ip.replace(/\/\d+$/, "")}` : ""}
                          {r.target_type ? <span className="hidden sm:inline"> · {r.target_type}</span> : null}
                        </div>
                      </div>
                    </button>

                    {isOpen && (
                      <div id={panel} className="mb-4 me-4 ms-11 space-y-3 rounded-md border border-border bg-surface p-3 sm:me-5">
                        {w.note && <p className="text-sm text-muted-foreground">{w.note}</p>}
                        {/* THE EXACT RECORD. Unchanged, for the day the readable version is not enough. */}
                        <KeyValueGrid
                          columns={2}
                          items={[
                            { label: "Recorded", value: <span className="font-mono text-xs break-all">{new Date(r.ts).toISOString()}</span> },
                            { label: "Action code", value: <span className="font-mono text-xs break-all">{r.action}</span> },
                            { label: "Actor", value: <span className="font-mono text-xs break-all">{r.actor_id || `(${r.actor_type ?? "system"})`}</span> },
                            { label: "Target", value: <span className="font-mono text-xs break-all">{r.target_id ? `${r.target_type ?? ""} ${r.target_id}`.trim() : "—"}</span> },
                            { label: "Source address", value: <span className="font-mono text-xs break-all">{r.ip || "—"}</span> },
                          ]}
                        />
                        {r.payload !== null && r.payload !== undefined && (
                          <details className="group">
                            <summary className="cursor-pointer select-none rounded-sm text-label text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                              Raw payload
                            </summary>
                            <pre className="mt-2 max-h-80 overflow-auto rounded-md border border-border bg-card p-2 font-mono text-2xs">
                              {JSON.stringify(r.payload, null, 2)}
                            </pre>
                          </details>
                        )}
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </Card>
    </PageShell>
  );
}
