"use client";

// Everything that happened to clients, newest first, from edged's /sessions/activity: connections, packages, devices,
// accounts, staff actions on clients and, where the Hotel module is on, room sign-ins and stays. edged leaves out what
// the reader may not read and every Hotel source on other sites; the chips follow what it reports.

import { useMemo, useState } from "react";
import Link from "next/link";
import { RowActions } from "@/components/ui/row-actions";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardFooter } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { DescriptionList, FilterChips, Pagination, SearchInput } from "@/components/ui/data";
import { FilterBar } from "@/components/ui/toolbar";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { DetailsDrawer } from "@/components/ui/details-drawer";
import { HelpList, HelpSection } from "@/components/help";
import { refreshingClass } from "@/components/ui/patterns";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { cn, formatDate } from "@/lib/utils";
import { activityWords, clientWords, type ActivityEvent } from "@/lib/client-activity-words";
import { Activity, AlertTriangle, RefreshCw } from "lucide-react";

type Resp = PagedFields & {
  data?: ActivityEvent[];
  categories?: Record<string, number>;
  problems?: number;
  sources?: { hotel?: boolean; room_sign_ins?: boolean; stays?: boolean; accounts?: boolean; staff_actions?: boolean };
};

const RANGES = [
  { value: "1h", label: "Last hour" },
  { value: "24h", label: "Last 24 hours" },
  { value: "7d", label: "Last 7 days" },
  { value: "30d", label: "Last 30 days" },
];

type Chip = "all" | "problems" | "connection" | "package" | "device" | "account" | "sign_in" | "stay" | "staff";

const TONE = { ok: "ok", info: "default", warning: "warn", error: "err" } as const;

const clock = (ms: number) => new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

export default function ClientActivityPage() {
  const [range, setRange] = useState("24h");
  // Fixed until the period changes or Refresh, so paging walks a stable window.
  const [anchor, setAnchor] = useState(() => Date.now());
  const [chip, setChip] = useState<Chip>("all");
  const [text, setText] = useState("");
  const [open, setOpen] = useState<ActivityEvent | null>(null);

  const path = useMemo(() => {
    const p = new URLSearchParams({ range, at: String(anchor) });
    if (chip === "problems") p.set("problems", "1");
    else if (chip !== "all") p.set("category", chip);
    return `/sessions/activity?${p.toString()}`;
  }, [range, anchor, chip]);
  const q = text.trim();
  const headers = useMemo(() => (q ? { "X-Client-Activity-Search": encodeURIComponent(q) } : undefined), [q]);

  const list = useServerPage<Resp>({ path, headers });
  const { resp, current, err, loading } = list;
  const rows = current && resp ? resp.data ?? [] : null;
  const cats = resp?.categories ?? {};
  const src = resp?.sources ?? {};
  const all = Object.values(cats).reduce((a, b) => a + b, 0);
  const known = !!resp;
  const n = (v: number | undefined) => (known ? v ?? 0 : undefined);

  const chipOptions: { value: Chip; label: React.ReactNode; count?: number; tone?: "warn" }[] = [
    { value: "all", label: "Everything", count: known ? all : undefined },
    { value: "problems", label: "Problems", count: known ? resp?.problems : undefined, tone: "warn" },
    { value: "connection", label: "Connections", count: n(cats.connection) },
    { value: "package", label: "Packages", count: n(cats.package) },
    { value: "device", label: "Devices", count: n(cats.device) },
    ...(src.accounts ? [{ value: "account" as Chip, label: "Accounts", count: n(cats.account) }] : []),
    ...(src.room_sign_ins ? [{ value: "sign_in" as Chip, label: "Room sign-ins", count: n(cats.sign_in) }] : []),
    ...(src.stays ? [{ value: "stay" as Chip, label: "Stays", count: n(cats.stay) }] : []),
    ...(src.staff_actions ? [{ value: "staff" as Chip, label: "Staff actions", count: n(cats.staff) }] : []),
  ];
  const refresh = () => { setOpen(null); setAnchor(Date.now()); };

  const filtered = chip !== "all" || q !== "";
  const total = list.total;

  return (
    <PageShell width="wide">
      <PageHeader
        title="Client activity"
        description="Everything that happened to clients, newest first."
        help={
          <HelpSection title="What is listed">
            <HelpList items={[
              "Connections and disconnections, with the reason",
              "Packages started and ended, and why",
              "Device replacements and released places",
              ...(src.accounts ? ["Client accounts created and verified"] : []),
              ...(src.hotel ? ["Room sign-in attempts (including those refused while a device's sign-in was paused) and stays"] : []),
              ...(src.staff_actions ? ["Staff actions on clients"] : []),
              "Failed voucher, account and code sign-ins are not recorded",
            ]} />
          </HelpSection>
        }
        actions={
          <div className="flex items-center gap-3">
            {rows !== null && (
              <span className="text-caption text-muted-foreground tabular">As of {clock(anchor)}</span>
            )}
            <Button variant="secondary" onClick={refresh} disabled={loading}>
              <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} />
              {loading ? "Reading…" : "Refresh"}
            </Button>
          </div>
        }
      />

      {rows !== null && <ErrorBanner err={err} className="mb-0" />}

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={
            <SearchInput value={text} onChange={setText} label="Search client activity"
              placeholder="Room, name, MAC, IP or package…" delay={300} />
          }
          filters={
            <Select aria-label="Period" className="h-9 w-full sm:w-44" value={range}
              onChange={(e) => { setRange(e.target.value); refresh(); }}>
              {RANGES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
            </Select>
          }
          actions={rows !== null && total !== null
            ? <span className="text-xs text-muted-foreground tabular">{`${total.toLocaleString()} ${total === 1 ? "event" : "events"}`}</span>
            : undefined}
        />
        <div className="border-b border-border px-4 py-2.5">
          <FilterChips<Chip> label="Show" value={chip} onChange={(c) => { setOpen(null); setChip(c); }} options={chipOptions} />
        </div>

        <div className={cn(loading && rows && refreshingClass)}>
          {rows === null ? (
            err ? (
              <EmptyState icon={<AlertTriangle />} title="Client activity could not be loaded"
                hint={err instanceof Error ? err.message : undefined}
                action={<Button variant="secondary" disabled={loading} onClick={() => void list.reload()}><RefreshCw /> Try again</Button>} />
            ) : <SkeletonRows rows={8} cols={4} />
          ) : rows.length === 0 ? (
            <EmptyState size="sm" icon={<Activity />} title={filtered ? "Nothing matches" : "No client activity in this period"}
              hint={filtered ? "Widen the period or clear the filters." : "Choose a longer period to look further back."}
              action={
                <div className="flex flex-wrap justify-center gap-2">
                  {filtered && <Button variant="secondary" size="sm" onClick={() => { setChip("all"); setText(""); }}>Clear filters</Button>}
                  {range !== "30d" && (
                    <Button variant="secondary" size="sm" onClick={() => { setRange(range === "7d" ? "30d" : "7d"); refresh(); }}>
                      {range === "7d" ? "Last 30 days" : "Last 7 days"}
                    </Button>
                  )}
                </div>
              } />
          ) : (
            <Table label="Client activity">
              <THead>
                <TR>
                  <TH>When</TH>
                  <TH>What happened</TH>
                  <TH>Client</TH>
                  <TH className="hidden md:table-cell">Device</TH>
                  <TH className="hidden lg:table-cell">Network · package</TH>
                  <TH><span className="sr-only">Actions</span></TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((r) => {
                  const w = activityWords(r);
                  return (
                    <TR key={r.id} onClick={() => setOpen(r)} selected={open?.id === r.id}>
                      <TD nowrap muted className="tabular">{formatDate(r.at)}</TD>
                      <TD className="min-w-56">
                        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                          <span className="font-medium text-foreground">{w.title}</span>
                          {(r.severity === "warning" || r.severity === "error") && (
                            <Badge tone={TONE[r.severity]}>{r.severity === "error" ? "Failure" : "Attention"}</Badge>
                          )}
                        </div>
                        {w.detail && <div className="mt-0.5 text-xs text-muted-foreground">{w.detail}</div>}
                      </TD>
                      <TD truncate title={clientWords(r)}>{clientWords(r)}</TD>
                      <TD nowrap muted className="hidden font-mono text-xs md:table-cell">
                        {r.mac || "—"}{r.ip ? <span className="block">{r.ip}</span> : null}
                      </TD>
                      <TD truncate muted className="hidden lg:table-cell">
                        {[r.network, r.package].filter(Boolean).join(" · ") || "—"}
                      </TD>
                      <TD className="w-0">
                        <RowActions subject={`${w.title}, ${formatDate(r.at)}`} primary={{ label: "Details", onSelect: () => setOpen(r) }} />
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
            <Pagination offset={list.offset} limit={list.pageSize} shown={rows.length} total={total}
              hasMore={list.hasMore} onChange={(o) => { setOpen(null); list.setOffset(o); }} />
          </CardFooter>
        )}
      </Card>

      <DetailsDrawer
        open={open !== null}
        onOpenChange={(v) => !v && setOpen(null)}
        eyebrow="Client activity"
        title={open ? activityWords(open).title : ""}
        description={open ? `${clientWords(open)} · ${formatDate(open.at)}` : undefined}
        badges={open && (open.severity === "warning" || open.severity === "error")
          ? <Badge tone={TONE[open.severity]}>{open.severity === "error" ? "Failure" : "Attention"}</Badge>
          : undefined}
      >
        {open && (
          <DescriptionList
            items={[
              { label: "What happened", value: activityWords(open).detail || "—" },
              { label: "Client", value: clientWords(open) },
              { label: "Device", value: <span className="font-mono text-xs">{open.mac || "—"}</span> },
              { label: "Address", value: <span className="font-mono text-xs">{open.ip || "—"}</span> },
              { label: "Network", value: open.network || "—" },
              { label: "Package", value: open.package || "—" },
              ...(open.actor ? [{ label: "By", value: open.actor }] : []),
              ...(open.stay_id ? [{ label: "Usage", value: <Link className="text-primary underline-offset-2 hover:underline" href={`/usage?stay=${encodeURIComponent(open.stay_id)}`}>Usage for this stay</Link> }] : []),
            ]}
          />
        )}
        {open && (
          <section className="mt-5 space-y-2">
            <h3 className="text-sm font-semibold">For support</h3>
            <DescriptionList
              items={[{ label: "Recorded (UTC)", value: <span className="font-mono text-xs">{new Date(open.at).toISOString()}</span> }]}
            />
          </section>
        )}
      </DetailsDrawer>
    </PageShell>
  );
}
