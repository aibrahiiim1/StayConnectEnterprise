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
//
// PAGED ON THE SERVER. The screen used to read the newest 500 entries and filter and count those in the
// browser, so on a busy appliance anything older was unreachable and the filter counts described the 500.
// The period, category, security filter and search now all run in edged, a page at a time; the filter counts
// are the server's count of the whole period. The search text travels in a header, not the URL.

import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { PageShell, PageHeader, Toolbar } from "@/components/ui/page";
import { Card, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { FilterChips, KeyValueGrid, Pagination, SearchInput } from "@/components/ui/data";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { refreshingClass } from "@/components/ui/patterns";
import { cn, formatDate } from "@/lib/utils";
import {
  auditWords, auditActor, AUDIT_CATEGORIES, MODULE_CATEGORIES, auditCategoriesFor, type AuditCategory,
} from "@/lib/audit-words";
import { ScrollText, ChevronRight, ShieldAlert, RefreshCw } from "lucide-react";
import { HelpList, HelpSection } from "@/components/help";
import { moduleHasHistory, useCapabilities } from "@/lib/capabilities";

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
type AuditResp = PagedFields & {
  data?: Row[];
  /** How many entries of each action code the period holds -- never narrowed by the category or search. */
  actions?: { action: string; count: number }[];
};

const RANGES = [
  { label: "Last 24 hours", hours: 24 },
  { label: "Last 7 days", hours: 24 * 7 },
  { label: "Last 30 days", hours: 24 * 30 },
  { label: "Everything", hours: 0 },
];

type Chip = "all" | "security" | AuditCategory;

export default function ActivityPage() {
  const [range, setRange] = useState(RANGES[2]);
  // The moment the period was measured from. Fixed until the period changes or the operator refreshes, so
  // paging walks one stable question instead of a window that slides with every request.
  const [anchor, setAnchor] = useState(() => Date.now());
  const [chip, setChip] = useState<Chip>("all");
  const [text, setText] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [names, setNames] = useState<Map<string, string>>(new Map());

  const period = `${range.label}|${anchor}`;
  // The per-action counts of the period, from the server. They are what turns a category, the security filter
  // and a search by readable title into the exact action codes the server filters by: the words live here, the
  // rows live there.
  const [counts, setCounts] = useState<{ period: string; byAction: Map<string, number> } | null>(null);
  const countsReady = counts?.period === period;
  const q = text.trim().toLowerCase();

  const codes = useMemo(() => {
    if (chip === "all" || !counts) return null;
    return [...counts.byAction.keys()].filter((a) => {
      const w = auditWords(a);
      return chip === "security" ? w.severity === "security" : w.category === chip;
    });
  }, [chip, counts]);
  const titled = useMemo(
    () => (q && counts ? [...counts.byAction.keys()].filter((a) => auditWords(a).title.toLowerCase().includes(q)) : []),
    [q, counts],
  );
  // A category or a title search needs this period's counts first. Until they arrive the request asks only for
  // the period (and its counts), and its rows are not shown as if they were the filtered answer.
  const needsCounts = chip !== "all" || q !== "";
  const filterReady = !needsCounts || countsReady;

  const path = useMemo(() => {
    const p = new URLSearchParams();
    if (range.hours) p.set("from", new Date(anchor - range.hours * 3600_000).toISOString());
    if (filterReady && codes) p.set("action", codes.join(","));
    return `/audit?${p.toString()}`;
  }, [range, anchor, filterReady, codes]);
  // THE SEARCH TRAVELS IN HEADERS, never the URL: the text, and the codes whose readable title matched it.
  const headers = useMemo(
    () => (q ? {
      "X-Audit-Search": encodeURIComponent(text.trim()),
      ...(filterReady && titled.length ? { "X-Audit-Search-Actions": titled.join(",") } : {}),
    } : undefined),
    [q, text, filterReady, titled],
  );

  const list = useServerPage<AuditResp>({ path, headers });
  const { resp, current, err, loading } = list;

  // Each answer carries the period's counts; keep them against the period they describe.
  useEffect(() => {
    if (!current || !resp?.actions) return;
    setCounts({ period, byAction: new Map(resp.actions.map((a) => [a.action, a.count])) });
  }, [current, resp, period]);

  const rows = current && filterReady && resp ? resp.data ?? [] : null;

  // THE OPERATOR DIRECTORY, ONCE. An audit row records who by id, which is the precise fact and the useless
  // one -- nobody scanning for "who changed the retention policy" can read a uuid. The exact id stays in the
  // expandable record; this is what puts a name on the summary line. Failure is silent on purpose: an
  // activity trail that would not render because a second request failed is worse than one showing ids.
  useEffect(() => {
    api.get<{ data: { id: string; display_name?: string; email?: string }[] }>("/operators")
      .then((r) => setNames(new Map((r.data ?? []).map((o) => [o.id, o.display_name || o.email || o.id]))))
      .catch(() => {});
  }, []);

  // Counts per filter, over the WHOLE period -- the server counted every entry, not the page on screen.
  const chipCounts = useMemo(() => {
    const out = { all: 0, security: 0, byCategory: new Map<AuditCategory, number>() };
    for (const [action, n] of countsReady ? counts!.byAction : []) {
      const w = auditWords(action);
      out.all += n;
      if (w.severity === "security") out.security += n;
      out.byCategory.set(w.category, (out.byCategory.get(w.category) ?? 0) + n);
    }
    return out;
  }, [counts, countsReady]);

  // The Hotel filter only where hospitality has history -- or where a Hotel entry is in the period, because a
  // filter must never be missing for rows that exist. The rows themselves are never filtered by module.
  const caps = useCapabilities();
  const categories = auditCategoriesFor((m) =>
    moduleHasHistory(caps, m)
    || AUDIT_CATEGORIES.some((c) => MODULE_CATEGORIES[c] === m && ((chipCounts.byCategory.get(c) ?? 0) > 0 || chip === c)));

  const chipOptions: { value: Chip; label: React.ReactNode; count?: number; tone?: "warn" }[] = [
    { value: "all", label: "Everything", count: countsReady ? chipCounts.all : undefined },
    // SECURITY EVENTS FIRST, because that is the filter somebody reaches for under pressure.
    {
      value: "security",
      label: <><ShieldAlert className="size-3.5" aria-hidden /> Security</>,
      count: countsReady ? chipCounts.security : undefined,
      tone: "warn",
    },
    ...categories.map((c) => ({
      value: c as Chip,
      label: c,
      count: countsReady ? chipCounts.byCategory.get(c) ?? 0 : undefined,
    })),
  ];

  const filtered = chip !== "all" || text.trim() !== "";
  const clearFilters = () => { setChip("all"); setText(""); };
  const refresh = () => setAnchor(Date.now());
  const total = list.total;

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<ScrollText />}
        eyebrow="System"
        title="Activity"
        description="Every change made to this appliance, by staff and by the system itself."
        help={
          <>
            <HelpSection title="What this record is">
              <p>
                The activity trail is written as things happen and is never edited or removed. Each line reads out what
                happened, who did it and when.
              </p>
            </HelpSection>
            <HelpSection title="Finding an entry">
              <HelpList
                items={[
                  "Search matches what happened, who, a source address or an id.",
                  <>The <strong>Security</strong> filter shows events such as a client&rsquo;s typed credentials being viewed, a backup leaving the appliance, or a refused unlicensed-mode attempt.</>,
                  "The whole period is searched and counted, newest first, a page at a time.",
                ]}
              />
            </HelpSection>
            <HelpSection title="The exact record">
              <p>
                Select a line to see the precise record: the recorded time, action code, actor and target ids, source
                address and raw payload.
              </p>
            </HelpSection>
          </>
        }
        actions={
          <Button variant="secondary" onClick={refresh} disabled={loading}>
            <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} />
            {loading ? "Reading…" : "Refresh"}
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
            delay={300}
          />
          <Select
            aria-label="Period"
            className="h-9 w-full sm:w-44"
            value={range.label}
            onChange={(e) => {
              setRange(RANGES.find((x) => x.label === e.target.value) ?? RANGES[2]);
              setAnchor(Date.now());
            }}
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
              {rows === null || total === null ? "Activity" : `${total.toLocaleString()} ${total === 1 ? "entry" : "entries"}`}
            </CardTitle>
            <CardDescription>{range.label} · newest first</CardDescription>
          </div>
        </CardHeader>

        <div className={cn(loading && rows && refreshingClass)}>
          {rows === null ? (
            err ? (
              <EmptyState icon={<ScrollText />} title="The activity trail could not be read"
                hint="It appears here once the appliance answers." />
            ) : <SkeletonRows rows={6} cols={3} />
          ) : rows.length === 0 ? (
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
              {rows.map((r, i) => {
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
        {rows && rows.length > 0 && (
          <div className="border-t border-border px-4 py-3 sm:px-5">
            <Pagination
              offset={list.offset}
              limit={list.pageSize}
              shown={rows.length}
              total={total}
              hasMore={list.hasMore}
              onChange={(o) => { setOpen(null); list.setOffset(o); }}
            />
          </div>
        )}
      </Card>
    </PageShell>
  );
}
