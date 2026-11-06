"use client";

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
import { Callout } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { DescriptionList, FilterChips, Pagination, ResultsCount, SearchInput } from "@/components/ui/data";
import { FilterBar } from "@/components/ui/toolbar";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { refreshingClass } from "@/components/ui/patterns";
import { cn, formatDate } from "@/lib/utils";
import {
  auditWords, auditActor, auditTarget, AUDIT_CATEGORIES, MODULE_CATEGORIES, OCCASIONAL_CATEGORIES, auditCategoriesFor,
  type AuditCategory,
} from "@/lib/audit-words";
import { AlertTriangle, ScrollText, ShieldAlert, RefreshCw } from "lucide-react";
import { HelpList, HelpSection } from "@/components/help";
import { moduleHasHistory, useCapabilities } from "@/lib/capabilities";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const NOT_RECORDED = "Not recorded";
const NO_ROWS: Row[] = [];

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
  actions?: { action: string; count: number }[];
};

const RANGES = [
  { label: "Last 24 hours", hours: 24 },
  { label: "Last 7 days", hours: 24 * 7 },
  { label: "Last 30 days", hours: 24 * 30 },
  { label: "All time", hours: 0 },
];

type Chip = "all" | "security" | AuditCategory;

const mono = (v: string) => <span className="break-all font-mono text-xs">{v}</span>;

export default function ActivityPage() {
  const [range, setRange] = useState(RANGES[2]);
  const [anchor, setAnchor] = useState(() => Date.now());
  const [chip, setChip] = useState<Chip>("all");
  const [text, setText] = useState("");
  const [actor, setActor] = useState("");
  const [open, setOpen] = useState<Row | null>(null);
  const [names, setNames] = useState<Map<string, string> | null>(null);

  const period = `${range.label}|${anchor}|${actor}`;
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
  const needsCounts = chip !== "all" || q !== "";
  const filterReady = !needsCounts || countsReady;

  const path = useMemo(() => {
    const p = new URLSearchParams();
    if (range.hours) p.set("from", new Date(anchor - range.hours * 3600_000).toISOString());
    else p.set("window", "all");
    if (actor) p.set("actor", actor);
    if (filterReady && codes) p.set("action", codes.join(","));
    return `/audit?${p.toString()}`;
  }, [range, anchor, actor, filterReady, codes]);
  const headers = useMemo(
    () => (q ? {
      "X-Audit-Search": encodeURIComponent(text.trim()),
      ...(filterReady && titled.length ? { "X-Audit-Search-Actions": titled.join(",") } : {}),
    } : undefined),
    [q, text, filterReady, titled],
  );

  const list = useServerPage<AuditResp>({ path, headers });
  const { resp, current, err, loading } = list;

  useEffect(() => {
    if (!current || !resp?.actions) return;
    setCounts({ period, byAction: new Map(resp.actions.map((a) => [a.action, a.count])) });
  }, [current, resp, period]);

  const fresh = current && filterReady && resp ? resp.data ?? NO_ROWS : null;

  const view = `${range.label}|${actor}|${chip}|${q}|${list.offset}`;
  const [lastGood, setLastGood] = useState<{ view: string; rows: Row[]; total: number | null } | null>(null);
  useEffect(() => {
    if (fresh) setLastGood({ view, rows: fresh, total: list.total });
  }, [fresh, view, list.total]);
  const kept = lastGood && lastGood.view === view ? lastGood : null;
  const rows = fresh ?? kept?.rows ?? null;
  const total = fresh ? list.total : kept?.total ?? null;
  const stale = !!err && fresh === null && rows !== null;
  const failed = !!err && rows === null;

  const roles = useOperatorRoles();
  const mayReadOperators = roles !== null && canRead("operators", roles);
  useEffect(() => {
    if (!mayReadOperators) return;
    api.get<{ data: { id: string; display_name?: string; email?: string }[] }>("/operators")
      .then((r) => setNames(new Map((r.data ?? []).map((o) => [o.id, o.display_name || o.email || o.id]))))
      .catch(() => {});
  }, [mayReadOperators]);
  const people = useMemo(
    () => (names ? [...names.entries()].sort((a, b) => a[1].localeCompare(b[1])) : []),
    [names],
  );

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

  const caps = useCapabilities();
  const present = (c: AuditCategory) => (chipCounts.byCategory.get(c) ?? 0) > 0 || chip === c;
  const categories = auditCategoriesFor((m) =>
    moduleHasHistory(caps, m)
    || AUDIT_CATEGORIES.some((c) => MODULE_CATEGORIES[c] === m && present(c)))
    .filter((c) => !OCCASIONAL_CATEGORIES.includes(c) || present(c));

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

  const filtered = chip !== "all" || text.trim() !== "" || actor !== "";
  const clearFilters = () => { setChip("all"); setText(""); setActor(""); };
  const refresh = () => setAnchor(Date.now());
  const who = (r: Row) => auditActor(r.actor_type, r.actor_id, names, { type: r.target_type, id: r.target_id });
  const openWords = open ? auditWords(open.action) : null;

  return (
    <PageShell width="wide">
      <PageHeader
        title="Activity"
        description="What staff and the appliance itself did here: changes, sign-ins, views of client data and refused attempts."
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
                  "Search matches what happened, a source address or an ID.",
                  "To see one person's entries, choose them under Who.",
                  <>The <strong>Security</strong> filter shows events such as staff accounts and roles changing, a client&rsquo;s typed credentials or a voucher code being shown, a backup leaving the appliance, or a refused unlicensed-mode attempt.</>,
                  "The whole period is searched and counted, newest first, a page at a time.",
                ]}
              />
            </HelpSection>
            <HelpSection title="The exact record">
              <p>
                Select a line to see the precise record: the recorded time, action code, actor and target IDs, source
                address and raw payload.
              </p>
            </HelpSection>
          </>
        }
        actions={
          <Button variant="secondary" onClick={refresh} disabled={loading}>
            <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} />
            {loading ? "Refreshing…" : "Refresh"}
          </Button>
        }
      />

      {stale && (
        <div className={FADE}>
          <Callout tone="warning">The activity trail could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={
            <SearchInput
              value={text}
              onChange={setText}
              label="Search the activity trail"
              placeholder="What happened, an address or an ID"
              delay={300}
            />
          }
          filters={
            <>
              {people.length > 0 && (
                <Select
                  aria-label="Who"
                  className="h-9 w-full sm:w-48"
                  value={actor}
                  onChange={(e) => setActor(e.target.value)}
                >
                  <option value="">Anyone</option>
                  {people.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
                </Select>
              )}
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
            </>
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
        <ResultsCount
          count={rows === null ? null : total ?? rows.length}
          more={total === null && list.hasMore}
          noun="entry"
          plural="entries"
        />

        <div className={cn(loading && rows && refreshingClass)}>
          {rows === null ? (
            failed ? (
              <div role="alert" className={FADE}>
                <EmptyState
                  size="sm"
                  icon={<AlertTriangle />}
                  title="The activity trail could not be loaded"
                  hint="Check the appliance is reachable, then try again. Nothing has been changed."
                  action={
                    <Button size="sm" variant="secondary" onClick={refresh} disabled={loading}>
                      <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
                    </Button>
                  }
                />
              </div>
            ) : <SkeletonRows rows={6} cols={3} />
          ) : rows.length === 0 ? (
            filtered ? (
              <EmptyState size="sm" icon={<ScrollText />} title="Nothing matches"
                hint="Widen the period or clear the filters."
                action={<Button variant="secondary" size="sm" onClick={clearFilters}>Clear filters</Button>} />
            ) : (
              <EmptyState size="sm" icon={<ScrollText />} title="No activity in this period"
                hint="Choose a longer period to look further back." />
            )
          ) : (
            <Table label="Activity" aria-label="Activity">
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
                  const person = who(r);
                  const target = auditTarget(r.target_type);
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
                          <span className="md:hidden"> · {person}</span>
                          {target ? <span className="hidden sm:inline"> · {target}</span> : null}
                        </div>
                      </TD>
                      <TD truncate className="hidden md:table-cell" title={person}>{person}</TD>
                      <TD nowrap muted className="tabular">{formatDate(r.ts)}</TD>
                      <TD nowrap muted className="hidden font-mono text-xs lg:table-cell">
                        {r.ip ? r.ip.replace(/\/\d+$/, "") : <span className="font-sans">{NOT_RECORDED}</span>}
                      </TD>
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
        title={openWords ? openWords.title : ""}
        description={open ? `${who(open)} · ${formatDate(open.ts)}` : undefined}
        badges={open && openWords && (
          <>
            {openWords.severity === "security" && <Badge tone="warn"><ShieldAlert className="size-3" aria-hidden /> Security</Badge>}
            <Badge tone="default">{openWords.category}</Badge>
          </>
        )}
      >
        {open && openWords && (
          <div className="space-y-5">
            {openWords.note && <p className="text-sm text-muted-foreground">{openWords.note}</p>}
            <DescriptionList
              items={[
                { label: "Recorded", value: mono(new Date(open.ts).toISOString()) },
                { label: "Action code", value: mono(open.action) },
                {
                  label: "Actor",
                  value: open.actor_id
                    ? mono(open.actor_id)
                    : <span className="text-sm">{open.actor_type === "system" || !open.actor_type ? "The appliance" : NOT_RECORDED}</span>,
                },
                {
                  label: "Target",
                  value: open.target_id
                    ? <span className="text-sm">{auditTarget(open.target_type) ?? "Target"} {mono(open.target_id)}</span>
                    : <span className="text-sm">{NOT_RECORDED}</span>,
                },
                { label: "Source address", value: open.ip ? mono(open.ip) : <span className="text-sm">{NOT_RECORDED}</span> },
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
