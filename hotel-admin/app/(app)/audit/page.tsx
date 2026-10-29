"use client";

// The trail is append-only; each row leads with a sentence, a person and a time, with the exact record one
// click away. Severity is assigned per action in lib/audit-words.ts and unrecognised actions show their code.
// Period, category, security filter and search run server-side in edged, a page at a time.

import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { canRead } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardFooter } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { DescriptionList, FilterChips, Pagination, ResultsCount, SearchInput } from "@/components/ui/data";
import { FilterBar } from "@/components/ui/toolbar";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { refreshingClass } from "@/components/ui/patterns";
import { cn, formatDate } from "@/lib/utils";
import {
  auditWords, auditActor, AUDIT_CATEGORIES, MODULE_CATEGORIES, auditCategoriesFor, type AuditCategory,
} from "@/lib/audit-words";
import { ScrollText, ShieldAlert, RefreshCw } from "lucide-react";
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
  const [open, setOpen] = useState<Row | null>(null);
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
    // "Everything" is asked for explicitly: the server's default is the last 30 days (audit AD-56).
    else p.set("window", "all");
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

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={
            <SearchInput
              value={text}
              onChange={setText}
              label="Search the activity trail"
              placeholder="What happened, who, an address or an ID"
              delay={300}
            />
          }
          filters={
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
          }
          actions={
            rows !== null && total !== null
              ? <span className="text-xs text-muted-foreground tabular">{`${total.toLocaleString()} ${total === 1 ? "entry" : "entries"}`}</span>
              : undefined
          }
        />
        <div className="border-b border-border px-4 py-2.5">
          <FilterChips<Chip> label="Show" value={chip} onChange={setChip} options={chipOptions} />
        </div>
        <ResultsCount count={rows?.length} noun="entry" plural="entries" />

        <div className={cn(loading && rows && refreshingClass)}>
          {rows === null ? (
            err ? (
              <EmptyState size="sm" icon={<ScrollText />} title="The activity trail could not be read"
                hint="It appears here once the appliance answers." />
            ) : <SkeletonRows rows={6} cols={3} />
          ) : rows.length === 0 ? (
            filtered ? (
              <EmptyState size="sm" icon={<ScrollText />} title="Nothing matches"
                hint="Widen the period or clear the search."
                action={<Button variant="secondary" size="sm" onClick={clearFilters}>Clear filters</Button>} />
            ) : (
              <EmptyState size="sm" icon={<ScrollText />} title="No activity in this period"
                hint="Choose a longer period to look further back." />
            )
          ) : (
            <Table label="Activity">
              <THead>
                <TR>
                  <TH>What happened</TH>
                  <TH className="hidden md:table-cell">Who</TH>
                  <TH>When</TH>
                  <TH className="hidden lg:table-cell">Source</TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((r, i) => {
                  const w = auditWords(r.action);
                  const who = auditActor(r.actor_type, r.actor_id, names);
                  return (
                    <TR key={`${r.ts}-${i}`} onClick={() => setOpen(r)} selected={open === r}>
                      <TD className="min-w-56">
                        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                          <span className="font-medium text-foreground">{w.title}</span>
                          {w.severity === "security" && (
                            <Badge tone="warn"><ShieldAlert className="size-3" aria-hidden /> Security</Badge>
                          )}
                        </div>
                        <div className="mt-0.5 text-xs text-muted-foreground">
                          {w.category}
                          <span className="md:hidden"> · {who}</span>
                          {r.target_type ? <span className="hidden sm:inline"> · {r.target_type}</span> : null}
                        </div>
                      </TD>
                      <TD truncate className="hidden md:table-cell" title={typeof who === "string" ? who : undefined}>{who}</TD>
                      <TD nowrap muted className="tabular">{formatDate(r.ts)}</TD>
                      <TD nowrap muted className="hidden font-mono text-xs lg:table-cell">{r.ip ? r.ip.replace(/\/\d+$/, "") : "—"}</TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          )}
        </div>
        {rows && rows.length > 0 && (
          <CardFooter>
            <Pagination
              offset={list.offset}
              limit={list.pageSize}
              shown={rows.length}
              total={total}
              hasMore={list.hasMore}
              onChange={(o) => { setOpen(null); list.setOffset(o); }}
            />
          </CardFooter>
        )}
      </Card>

      <DetailsDrawer
        open={open !== null}
        onOpenChange={(v) => !v && setOpen(null)}
        eyebrow="Activity entry"
        title={open ? auditWords(open.action).title : ""}
        description={open ? `${auditActor(open.actor_type, open.actor_id, names)} · ${formatDate(open.ts)}` : undefined}
        badges={open && (
          <>
            {auditWords(open.action).severity === "security" && <Badge tone="warn"><ShieldAlert className="size-3" aria-hidden /> Security</Badge>}
            <Badge tone="default">{auditWords(open.action).category}</Badge>
          </>
        )}
      >
        {open && (
          <div className="space-y-5">
            {auditWords(open.action).note && <p className="text-sm text-muted-foreground">{auditWords(open.action).note}</p>}
            <DescriptionList
              items={[
                { label: "Recorded", value: <span className="break-all font-mono text-xs">{new Date(open.ts).toISOString()}</span> },
                { label: "Action code", value: <span className="break-all font-mono text-xs">{open.action}</span> },
                { label: "Actor", value: <span className="break-all font-mono text-xs">{open.actor_id || `(${open.actor_type ?? "system"})`}</span> },
                { label: "Target", value: <span className="break-all font-mono text-xs">{open.target_id ? `${open.target_type ?? ""} ${open.target_id}`.trim() : "—"}</span> },
                { label: "Source address", value: <span className="break-all font-mono text-xs">{open.ip || "—"}</span> },
              ]}
            />
            {open.payload !== null && open.payload !== undefined && (
              <DetailsSection title="Payload">
                <details className="group">
                  <summary className="cursor-pointer select-none rounded-sm text-label text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                    Raw payload
                  </summary>
                  <pre className="mt-2 max-h-96 overflow-auto rounded-md border border-border bg-surface p-3 font-mono text-2xs">
                    {JSON.stringify(open.payload, null, 2)}
                  </pre>
                </details>
              </DetailsSection>
            )}
          </div>
        )}
      </DetailsDrawer>
    </PageShell>
  );
}
