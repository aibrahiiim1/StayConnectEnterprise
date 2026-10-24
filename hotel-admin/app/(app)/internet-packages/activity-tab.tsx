"use client";

// Every grant, whatever produced it, from the purchase record forward. "Offered" appears only when a portal
// offer really existed, and no time is shown that was not recorded.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { Activity, BarChart3, Clock, Database, ExternalLink, PackageCheck, Users } from "lucide-react";
import { api, ListResp } from "@/lib/api";
import {
  getActivity, priceText, statusWords, whoWords, endReasonText, SOURCE_LABELS,
  type ActivityQuery, type ActivityRange, type ActivityResponse, type ActivityRow, type ActivityStatus,
  type PackageSummary,
} from "@/lib/api/commerce";
import { StatCard, StatGrid } from "@/components/ui/page";
import { Card, CardFooter, SectionCard } from "@/components/ui/card";
import { FilterBar } from "@/components/ui/toolbar";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Input, Select } from "@/components/ui/input";
import { Segmented } from "@/components/ui/tabs";
import { Sheet, SheetContent, SheetHeader, SheetBody, SheetSection } from "@/components/ui/sheet";
import { FilterChips, KeyValueGrid, MetricStrip, Pagination, SearchInput, Timeline } from "@/components/ui/data";
import { BarList } from "@/components/ui/chart";
import { focusSheetItself } from "@/components/commerce/sheet-focus";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { formatBytes } from "@/lib/bytes";
import { formatDuration } from "@/lib/units";
import { formatDate, formatRelative } from "@/lib/utils";
import type { TabProps } from "./packages-tab";
import { moduleHasHistory, moduleLicensed, useCapabilities } from "@/lib/capabilities";

const PAGE = 25;

function When({ at }: { at?: string | null }) {
  if (!at) return <span className="text-muted-foreground">—</span>;
  return <time dateTime={at} title={formatDate(at)}>{formatRelative(at)}</time>;
}

/** datetime-local value to RFC 3339, or undefined when incomplete. */
function localToISO(v: string): string | undefined {
  if (!v) return undefined;
  const t = new Date(v);
  return Number.isFinite(t.getTime()) ? t.toISOString() : undefined;
}

const HOSPITALITY_SOURCES = ["CHECKOUT_GRACE", "EMERGENCY_GRACE", "POST_STAY_CONVERSION", "CROSS_PMS_TRANSFER"];
const IDENTITY_MODULES = ["email_otp", "sms_otp", "whatsapp_otp", "social_login"];

export function ActivityTab({ guard, setErr }: TabProps) {
  const [range, setRange] = useState<ActivityRange>("7d");
  const [customFrom, setCustomFrom] = useState("");
  const [customTo, setCustomTo] = useState("");
  const [packageID, setPackageID] = useState("");
  const [source, setSource] = useState("");
  const [status, setStatus] = useState<ActivityStatus>("all");
  const [q, setQ] = useState("");
  const [offset, setOffset] = useState(0);

  // Hospitality sources are offered only where hospitality is licensed or has records, and the identity
  // source only where an identity module is licensed; the chosen filter always stays listed.
  const caps = useCapabilities();
  const stays = moduleHasHistory(caps, "hospitality");
  const identity = IDENTITY_MODULES.some((m) => moduleLicensed(caps, m));
  const offerSource = (v: string) =>
    v === source
    || (HOSPITALITY_SOURCES.includes(v) ? stays : v === "OTP_SOCIAL_DEFAULT" ? identity : true);

  const [resp, setResp] = useState<ActivityResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [packages, setPackages] = useState<PackageSummary[]>([]);
  const [open, setOpen] = useState<ActivityRow | null>(null);

  useEffect(() => {
    api.get<ListResp<PackageSummary>>("/commercial-packages")
      .then((r) => setPackages(r.data ?? []))
      .catch(() => setPackages([]));
  }, []);

  const customReady = range !== "custom" || (!!localToISO(customFrom) && !!localToISO(customTo));

  const load = useCallback(async () => {
    if (!customReady) return;
    const query: ActivityQuery = {
      range, from: localToISO(customFrom), to: localToISO(customTo),
      package_id: packageID || undefined, source: source || undefined, status, q, limit: PAGE, offset,
    };
    setLoading(true); setLoadErr(null);
    try {
      setResp(await getActivity(query));
    } catch (e) {
      if (!guard(e)) { setLoadErr(e); setErr(null); }
    } finally { setLoading(false); }
  }, [range, customFrom, customTo, packageID, source, status, q, offset, customReady, guard, setErr]);
  useEffect(() => { load(); }, [load]);

  const reset = <T,>(set: (v: T) => void) => (v: T) => { setOffset(0); set(v); };

  const s = resp?.summary;
  const top = s?.by_package?.[0];
  const rows = resp?.data ?? [];

  return (
    <div className="space-y-5">
      <FilterBar
        filters={
          <Segmented<ActivityRange>
            label="Period"
            value={range}
            onChange={reset(setRange)}
            options={[
              { value: "24h", label: "24 hours" }, { value: "7d", label: "7 days" },
              { value: "30d", label: "30 days" }, { value: "custom", label: "Custom" },
            ]}
          />
        }
        search={stays ? (
          // The server searches only rooms and reservations, so the box exists only where stays do.
          <SearchInput value={q} onChange={reset(setQ)} delay={300}
            placeholder="Room or reservation" label="Search by room or reservation" />
        ) : undefined}
      >
        {range === "custom" && (
          <>
            <Input type="datetime-local" aria-label="From" className="w-full sm:w-52" value={customFrom}
              onChange={(e) => { setOffset(0); setCustomFrom(e.target.value); }} />
            <Input type="datetime-local" aria-label="To" className="w-full sm:w-52" value={customTo}
              onChange={(e) => { setOffset(0); setCustomTo(e.target.value); }} />
          </>
        )}
        <Select aria-label="Package" className="w-full sm:w-48" value={packageID}
          onChange={(e) => { setOffset(0); setPackageID(e.target.value); }}>
          <option value="">All packages</option>
          {packages.map((p) => <option key={p.package_id} value={p.package_id}>{p.name || p.code}</option>)}
        </Select>
        <Select aria-label="How it was given" className="w-full sm:w-52" value={source}
          onChange={(e) => { setOffset(0); setSource(e.target.value); }}>
          <option value="">Every way</option>
          {Object.entries(SOURCE_LABELS).filter(([v]) => offerSource(v)).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </Select>
      </FilterBar>
      {range === "custom" && !customReady && (
        <p className="-mt-3 text-xs text-muted-foreground">Choose both a start and an end to see a custom period.</p>
      )}

      <ErrorBanner err={loadErr} />

      <StatGrid cols={4}>
        <StatCard label="Packages still valid" value={s ? s.active_now.toLocaleString() : "—"} icon={<Users />}
          hint="Given out, not yet ended" />
        <StatCard label="Access started" value={s ? s.started_in_range.toLocaleString() : "—"} icon={<PackageCheck />}
          hint={s ? `${s.in_range.toLocaleString()} in use in the period` : undefined} />
        <StatCard label="Data used" value={s ? formatBytes(s.data_bytes) : "—"} icon={<Database />}
          hint="On these grants' connections" />
        <StatCard label="Most given package" value={top ? top.name : "—"} icon={<BarChart3 />}
          hint={top ? `${top.grants.toLocaleString()} grant${top.grants === 1 ? "" : "s"}` : "Nothing in this period"} />
      </StatGrid>

      {s && s.undated > 0 && (
        <Callout tone="neutral" title="Some records have no time">
          {s.undated.toLocaleString()} purchase record{s.undated === 1 ? " has" : "s have"} neither an activation nor
          a taken offer, so {s.undated === 1 ? "it" : "they"} cannot be placed in a period and {s.undated === 1 ? "is" : "are"} not
          counted here. No time has been guessed for {s.undated === 1 ? "it" : "them"}.
        </Callout>
      )}

      <div className="grid gap-5 lg:grid-cols-2">
        <SectionCard title="Grants by package">
          {s ? <BarList items={s.by_package.slice(0, 8).map((p) => ({ key: p.package_id, name: p.name, value: p.grants }))}
            emptyLabel="No package was in use in this period" /> : <SkeletonRows rows={3} cols={1} />}
        </SectionCard>
        <SectionCard title="How access was given">
          {s ? <BarList items={s.by_source.map((x) => ({ key: x.source, name: x.label, value: x.grants, tone: "info" as const }))}
            emptyLabel="Nothing was given in this period" /> : <SkeletonRows rows={3} cols={1} />}
        </SectionCard>
      </div>

      <Card className="overflow-hidden">
        <FilterBar
          inset
          filters={
            <FilterChips<ActivityStatus>
              label="Status"
              value={status}
              onChange={reset(setStatus)}
              options={[
                { value: "all", label: "All", count: s ? s.status_counts.active + s.status_counts.ended + s.status_counts.other : undefined },
                { value: "active", label: "In use", count: s?.status_counts.active, tone: "ok" },
                { value: "ended", label: "Ended", count: s?.status_counts.ended },
                { value: "other", label: "Not started or no access", count: s?.status_counts.other, tone: "warn" },
              ]}
            />
          }
        />
        {loading && !resp ? (
          <SkeletonRows rows={6} cols={6} />
        ) : rows.length === 0 ? (
          <EmptyState size="sm" icon={<Activity />} title="Nothing in this period"
            hint="Widen the period or clear a filter." />
        ) : (
          <Table label="Client activity">
            <THead><TR>
              <TH>When</TH><TH>Package</TH><TH>Client</TH><TH className="hidden md:table-cell">How it was given</TH>
              <TH>Status</TH><TH numeric>Data used</TH><TH numeric className="hidden sm:table-cell">Devices</TH>
            </TR></THead>
            <TBody>
              {rows.map((r) => {
                const st = statusWords(r.status);
                return (
                  <TR key={r.purchase_id} onClick={() => setOpen(r)}>
                    <TD nowrap><When at={r.occurred_at} /></TD>
                    <TD truncate title={r.package_name} className="max-w-[14rem]">
                      <span className="font-medium">{r.package_name}</span>
                      <div className="text-xs text-muted-foreground">{priceText(r.price_minor, r.currency, r.currency_exponent)}</div>
                    </TD>
                    <TD nowrap>
                      <div>{whoWords(r)}</div>
                      {r.room && r.pms_interface && <div className="text-xs text-muted-foreground">{r.pms_interface}</div>}
                    </TD>
                    <TD nowrap muted className="hidden md:table-cell">{r.source_label}</TD>
                    <TD nowrap>
                      <StatusBadge tone={st.tone}>{st.label}</StatusBadge>
                      {r.online_now && <div className="mt-0.5 text-xs text-success">Online now</div>}
                    </TD>
                    <TD numeric>{r.sessions > 0 ? formatBytes(r.bytes_down + r.bytes_up) : "—"}</TD>
                    <TD numeric className="hidden sm:table-cell">{r.devices || "—"}</TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
        {resp && rows.length > 0 && (
          <CardFooter>
            <Pagination offset={offset} limit={PAGE} shown={rows.length} total={resp.meta.total} onChange={setOffset} />
          </CardFooter>
        )}
      </Card>

      <Sheet open={open !== null} onOpenChange={(v) => !v && setOpen(null)}>
        <SheetContent width="md" onOpenAutoFocus={focusSheetItself}>
          {open && <ActivityRecord r={open} />}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function ActivityRecord({ r }: { r: ActivityRow }) {
  const st = statusWords(r.status);
  const ended = endReasonText(r.end_reason);

  // Only recorded moments; the offer records no time, so none is shown.
  const events: { at: string; title: string; body?: string; tone: "ok" | "info" | "neutral" | "warn" | "err" }[] = [];
  if (r.had_offer && r.offer_taken_at) events.push({ at: r.offer_taken_at, title: "Offer taken on the portal", tone: "info" });
  if (r.started_at) events.push({ at: r.started_at, title: "Access started", tone: "ok" });
  if (r.first_session_at) events.push({ at: r.first_session_at, title: "First device connected", tone: "info" });
  if (r.last_session_at && r.last_session_at !== r.first_session_at) {
    events.push({ at: r.last_session_at, title: r.online_now ? "Latest connection (online now)" : "Last activity", tone: "neutral" });
  }
  if (r.ended_at) events.push({ at: r.ended_at, title: "Access ended", body: ended ?? undefined, tone: "neutral" });
  events.sort((a, b) => new Date(a.at).getTime() - new Date(b.at).getTime());

  return (
    <>
      <SheetHeader
        icon={<PackageCheck />}
        eyebrow="Client activity"
        title={r.package_name}
        description={`${whoWords(r)}${r.room && r.pms_interface ? ` · ${r.pms_interface}` : ""}`}
        badges={<>
          <Badge tone={st.tone} dot>{st.label}</Badge>
          <Badge tone="neutral">{r.source_label}</Badge>
          {r.emergency_grace && <Badge tone="warn">Emergency Grace Period</Badge>}
        </>}
      />
      <SheetBody>
        <MetricStrip items={[
          { label: "Data used", value: r.sessions > 0 ? formatBytes(r.bytes_down + r.bytes_up) : "—" },
          { label: "Devices", value: r.devices },
          { label: "Connections", value: r.sessions },
          { label: "Online time", value: r.consumed_online_seconds ? formatDuration(r.consumed_online_seconds) : "—" },
        ]} />

        {r.status === "NOT_GRANTED" && (
          <Callout tone="warning" title="No access was given">
            This purchase is recorded as {r.purchase_state.replace(/_/g, " ").toLowerCase()} and produced no internet
            access.
          </Callout>
        )}

        <SheetSection title="What happened">
          <Timeline emptyLabel="No time is recorded for this grant yet"
            items={events.map((e, i) => ({
              key: String(i), title: e.title, tone: e.tone, body: e.body,
              when: <time dateTime={e.at} title={formatDate(e.at)}>{formatDate(e.at)}</time>,
            }))} />
        </SheetSection>

        <SheetSection title="The grant">
          <KeyValueGrid items={[
            { label: "Package", value: r.package_name, hint: `Version ${r.revision_no}` },
            { label: "Price", value: priceText(r.price_minor, r.currency, r.currency_exponent) },
            { label: "How it was given", value: r.source_label },
            { label: "Portal offer", value: r.had_offer ? "Chosen from an offer on the portal" : "Given without a portal offer" },
            ...(r.room ? [
              { label: "Room", value: `Room ${r.room}`, hint: r.pms_interface },
              { label: "Reservation", value: r.reservation || "—" },
            ] : []),
            { label: "Service plan", value: r.service_plan || "—" },
            { label: "Data allowance", value: r.quota_bytes ? formatBytes(r.quota_bytes) : "No data limit",
              hint: r.consumed_data_bytes != null && r.quota_bytes ? `${formatBytes(r.consumed_data_bytes)} counted against it` : undefined },
            { label: "Signed in with", value: r.sign_in_method ? r.sign_in_method.replace(/_/g, " ").toLowerCase() : "—" },
            { label: "Why it ended", value: ended ?? (r.status === "TERMINATED" ? "Not recorded" : "Still running") },
          ]} />
        </SheetSection>

        {r.usage_href && (
          <Link href={r.usage_href}
            className="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline">
            <ExternalLink className="size-4" /> Open this stay&rsquo;s usage
          </Link>
        )}

        <SheetSection title="For support">
          <KeyValueGrid items={[
            { label: "Purchase", value: <MonoId value={r.purchase_id} /> },
            { label: "Access grant", value: <MonoId value={r.entitlement_id} /> },
            { label: "Package version", value: <MonoId value={r.package_revision_id} /> },
            { label: "Recorded", value: r.occurred_at ? <span className="inline-flex items-center gap-1"><Clock className="size-3.5" /><When at={r.occurred_at} /></span> : "—" },
          ]} />
        </SheetSection>
      </SheetBody>
    </>
  );
}
