"use client";

// The trail is append-only; each row leads with a sentence, a person and a time, with the exact record one
// click away. Severity is assigned per action in lib/audit-words.ts and unrecognised actions show their code.
// Period, category, security filter and search run server-side in edged, a page at a time.

import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { canRead } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { PageShell, PageHeader, Toolbar } from "@/components/ui/page";
import { Card, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { FilterChips, KeyValueGrid, Pagination, ResultsCount, SearchInput } from "@/components/ui/data";
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
  /** Entries per action code for the period; never narrowed by the category or search. */
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
  // Fixed until the period changes or the operator refreshes, so paging walks a stable window.
  const [anchor, setAnchor] = useState(() => Date.now());
  const [chip, setChip] = useState<Chip>("all");
  const [text, setText] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [names, setNames] = useState<Map<string, string>>(new Map());

  const period = `${range.label}|${anchor}`;
  // The server's per-action counts turn a category, the security filter or a title search into the action codes it filters by.
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
  // Until the counts arrive, the unfiltered rows are not shown as the filtered answer.
  const needsCounts = chip !== "all" || q !== "";
  const filterReady = !needsCounts || countsReady;

  const path = useMemo(() => {
    const p = new URLSearchParams();
    if (range.hours) p.set("from", new Date(anchor - range.hours * 3600_000).toISOString());
    if (filterReady && codes) p.set("action", codes.join(","));
    return `/audit?${p.toString()}`;
  }, [range, anchor, filterReady, codes]);
  // The search text travels in headers, never the URL.
  const headers = useMemo(
    () => (q ? {
      "X-Audit-Search": encodeURIComponent(text.trim()),
      ...(filterReady && titled.length ? { "X-Audit-Search-Actions": titled.join(",") } : {}),
    } : undefined),
    [q, text, filterReady, titled],
  );

  const list = useServerPage<AuditResp>({ path, headers });
  const { resp, current, err, loading } = list;

  // Keep each answer's counts against the period they describe.
  useEffect(() => {
    if (!current || !resp?.actions) return;
    setCounts({ period, byAction: new Map(resp.actions.map((a) => [a.action, a.count])) });
  }, [current, resp, period]);

  const rows = current && filterReady && resp ? resp.data ?? [] : null;

  // Operator names for the summary line; failure is silent so the trail still renders with ids. Only roles
  // that may read the directory ask, since others would get a 403 on every visit.
  const roles = useOperatorRoles();
  const mayReadOperators = roles !== null && canRead("operators", roles);
  useEffect(() => {
    if (!mayReadOperators) return;
    api.get<{ data: { id: string; display_name?: string; email?: string }[] }>("/operators")
      .then((r) => setNames(new Map((r.data ?? []).map((o) => [o.id, o.display_name || o.email || o.id]))))
      .catch(() => {});
  }, [mayReadOperators]);

  // Counts cover the whole period, read from the current answer so they arrive with the rows.
  const shownCounts = useMemo(
    () => (current && resp?.actions
      ? new Map(resp.actions.map((a) => [a.action, a.count]))
      : countsReady ? counts!.byAction : null),
    [current, resp, counts, countsReady],
  );
  const chipCounts = useMemo(() => {
    const out = { all: 0, security: 0, byCategory: new Map<AuditCategory, number>() };
    for (const [action, n] of shownCounts ?? []) {
      const w = auditWords(action);
      out.all += n;
      if (w.severity === "security") out.security += n;
      out.byCategory.set(w.category, (out.byCategory.get(w.category) ?? 0) + n);
    }
    return out;
  }, [shownCounts]);

  // The Hotel filter shows where hospitality has history or a Hotel entry is in the period; rows are never filtered by module.
  const caps = useCapabilities();
  const categories = auditCategoriesFor((m) =>
    moduleHasHistory(caps, m)
    || AUDIT_CATEGORIES.some((c) => MODULE_CATEGORIES[c] === m && ((chipCounts.byCategory.get(c) ?? 0) > 0 || chip === c)));

  const chipOptions: { value: Chip; label: React.ReactNode; count?: number; tone?: "warn" }[] = [
    { value: "all", label: "Everything", count: shownCounts ? chipCounts.all : undefined },
    {
      value: "security",
      label: <><ShieldAlert className="size-3.5" aria-hidden /> Security</>,
      count: shownCounts ? chipCounts.security : undefined,
      tone: "warn",
    },
    ...categories.map((c) => ({
      value: c as Chip,
      label: c,
      count: shownCounts ? chipCounts.byCategory.get(c) ?? 0 : undefined,
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
        <ResultsCount count={rows?.length} noun="entry" plural="entries" />
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
