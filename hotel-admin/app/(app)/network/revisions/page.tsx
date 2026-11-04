"use client";

import { useEffect, useRef, useState } from "react";
import {
  api, inBackground, ListResp,
  NetRevision, NetRevisionDetail,
} from "@/lib/api";
import { Card, CardFooter } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageShell, PageHeader } from "@/components/ui/page";
import { DescriptionList, FilterChips, Timeline } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { FilterBar } from "@/components/ui/toolbar";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { LiveStatus, PendingChangeBanner, ReadOnlyNotice, useSecondsLeft } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { AlertTriangle, ChevronRight, History, RefreshCw } from "lucide-react";
import { errMsg, formatDate } from "@/lib/utils";
import { usePoll } from "@/lib/use-poll";
import { HelpList, HelpSection } from "@/components/help";
import {
  HealthCheckList, RevisionStateBadge, ValidationIssueList, revisionStateLabel, useNetworkAccess,
} from "@/components/network/shared";
import { isReauth, useNetworkChangeStepUp } from "@/components/network/change-step-up";

const NEEDS = new Set(["pending_confirmation", "rolled_back", "failed"]);
const PREVIEW = 15;

const PHASE_WORDS: Record<string, string> = {
  validate: "Configuration checked",
  generate: "Configuration files written",
  apply: "Configuration applied",
  bridge_ownership: "Bridge ownership checked",
  bridges_left_alone: "Bridges left unchanged",
  kea: "DHCP server configured",
  nft: "Firewall rules loaded",
  unbound: "DNS resolver configured",
  portal_address: "Sign-in page address set",
  readdress: "Addresses changed",
  kea_disk: "DHCP settings saved",
  rollback: "Rolled back",
  rollback_bridges: "Bridges rolled back",
  rollback_nft: "Firewall rules rolled back",
  rollback_complete: "Roll back complete",
  rollback_incomplete: "Roll back incomplete",
  rollback_incomplete_cleared: "Incomplete roll back cleared",
  rollback_incomplete_clear_failed: "Incomplete roll back could not be cleared",
  boot_reconcile: "Checked after a restart",
  boot_reconcile_blocked: "Check after a restart was blocked",
  boot_reclaim: "Recovered after a restart",
};

function clock(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

function EventDetail({ detail }: { detail: unknown }) {
  if (detail === null || detail === undefined) return null;
  if (typeof detail !== "object") return <span className="break-words font-mono text-caption">{String(detail)}</span>;
  const entries = Object.entries(detail as Record<string, unknown>);
  if (entries.length === 0) return null;
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-caption">
      {entries.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k.replace(/_/g, " ")}</dt>
          <dd className="min-w-0 break-words font-mono">{typeof v === "string" ? v : JSON.stringify(v)}</dd>
        </div>
      ))}
    </dl>
  );
}

export default function RevisionsPage() {
  const [rows, setRows] = useState<NetRevision[] | null>(null);
  const { known, writable } = useNetworkAccess();
  const [err, setErr] = useState<string | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [busy, setBusy] = useState<{ id: string; kind: "confirm" | "rollback" } | null>(null);
  const [openSnap, setOpenSnap] = useState<NetRevision | null>(null);
  const [detail, setDetail] = useState<Record<string, { state: string; data: NetRevisionDetail }>>({});
  const [detailErr, setDetailErr] = useState<string | null>(null);
  const [filter, setFilter] = useState<"needs-attention" | "all">("all");
  const [showAll, setShowAll] = useState(false);
  const toast = useToast();

  const activeSeq = Math.max(0, ...(rows ?? []).filter((r) => r.state === "active").map((r) => r.seq));
  const needs = (r: NetRevision) => r.state === "pending_confirmation" || (NEEDS.has(r.state) && r.seq > activeSeq);
  const filtered = (rows ?? []).filter((r) => filter === "all" || needs(r));
  const visible = showAll ? filtered : filtered.slice(0, PREVIEW);
  const needsCount = (rows ?? []).filter(needs).length;
  const pending = (rows ?? []).find((r) => r.state === "pending_confirmation") ?? null;
  const open = openSnap ? (rows ?? []).find((r) => r.id === openSnap.id) ?? openSnap : null;

  async function load() {
    setRefreshing(true);
    try {
      setRows((await api.get<ListResp<NetRevision>>("/network/revisions")).data ?? []);
      setLoadErr(null);
      setUpdatedAt(Date.now());
    } catch (e) { setLoadErr(errMsg(e)); }
    finally { setRefreshing(false); }
  }

  useEffect(() => {
    load();
  }, []);

  usePoll(() => load(), pending ? 5000 : 30000, { enabled: busy === null && rows !== null });

  const left = useSecondsLeft(pending?.confirm_deadline ?? null);
  const reloadedFor = useRef<string | null>(null);
  useEffect(() => {
    if (pending && left === 0 && reloadedFor.current !== pending.id) {
      reloadedFor.current = pending.id;
      const t = setTimeout(() => void inBackground(load), 2000);
      return () => clearTimeout(t);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [left, pending]);

  async function fetchDetail(r: NetRevision) {
    setDetailErr(null);
    try {
      const d = await api.get<NetRevisionDetail>(`/network/revisions/${r.id}`);
      setDetail((m) => ({ ...m, [r.id]: { state: r.state, data: d } }));
    } catch (e) { setDetailErr(errMsg(e)); }
  }

  function openDetail(r: NetRevision) {
    setOpenSnap(r);
    setDetailErr(null);
    if (detail[r.id]?.state !== r.state) void fetchDetail(r);
  }

  useEffect(() => {
    if (open && detail[open.id] && detail[open.id].state !== open.state) void fetchDetail(open);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open?.id, open?.state]);

  const stepUp = useNetworkChangeStepUp();

  function onConfirm(id: string) {
    stepUp.ask("confirm", async (password) => {
      setBusy({ id, kind: "confirm" }); setErr(null);
      try {
        await api.post(`/network/revisions/${id}/confirm`, { password });
        toast.success("Change kept", "It is now the active configuration.");
        setOpenSnap(null);
        await load();
      }
      catch (e) { if (isReauth(e)) throw e; setErr(errMsg(e)); }
      finally { setBusy(null); }
    });
  }
  function onRollback(id: string) {
    stepUp.ask("rollback", async (password) => {
      setBusy({ id, kind: "rollback" }); setErr(null);
      try {
        await api.post(`/network/revisions/${id}/rollback`, { password });
        toast.success("Change rolled back", "The previous configuration is back in place.");
        setOpenSnap(null);
        await load();
      }
      catch (e) { if (isReauth(e)) throw e; setErr(errMsg(e)); }
      finally { setBusy(null); }
    });
  }

  const cached = open ? detail[open.id] : undefined;
  const d = cached && open && cached.state === open.state ? cached.data : undefined;
  const hidden = filtered.length - visible.length;
  const openLeft = open?.state === "pending_confirmation" && open.id === pending?.id ? left : null;

  return (
    <PageShell width="wide">
      <PageHeader
        title="Config history"
        description="Every network validate and apply, with what it checked and how it ended."
        meta={rows !== null && (
          <LiveStatus
            updatedAt={updatedAt}
            refreshing={refreshing}
            onRefresh={() => void load()}
            intervalSeconds={pending ? 5 : 30}
            error={!!loadErr}
          />
        )}
        help={
          <>
            <HelpSection title="What a revision is">
              <p>
                Each validate and apply of the client network configuration is recorded as a numbered revision, with its
                validation result, apply events and health checks. Select a row to see them.
              </p>
            </HelpSection>
            <HelpSection title="Why nothing is ever deleted">
              <p>
                A revision is what a rollback returns <strong>to</strong>, and what the current configuration&rsquo;s
                history points at. Removing one would leave a misleading gap, so the list is filtered and folded instead.
              </p>
            </HelpSection>
            <HelpSection title="States that need attention">
              <HelpList
                items={[
                  <><strong>Pending confirmation</strong> — live now; keep it or it rolls back automatically.</>,
                  <><strong>Rolled back</strong> — the previous configuration was put back.</>,
                  <><strong>Failed</strong> — the apply did not complete; see the failure reason.</>,
                  "Only failures and roll backs newer than the active configuration count as needing attention.",
                ]}
              />
            </HelpSection>
          </>
        }
      />

      {known && !writable && <ReadOnlyNotice>Your role can view the history but not keep or roll back a change.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      {pending && (
        <PendingChangeBanner
          title={`Revision #${pending.seq} — confirm or it rolls back automatically`}
          description={
            pending.summary ||
            "This change is live now. Keep it to make it the active configuration; if nobody does in time, the appliance puts the previous configuration back on its own."
          }
          deadline={pending.confirm_deadline ?? null}
          canAct={writable}
          busy={busy?.id === pending.id ? busy.kind : null}
          onConfirm={() => onConfirm(pending.id)}
          onRollback={() => onRollback(pending.id)}
        />
      )}

      {/* No delete: a revision is a rollback target and the current configuration's provenance. */}
      <Card className="overflow-hidden">
        {rows !== null && rows.length > 0 && (
          <FilterBar
            inset
            filters={
              <FilterChips
                label="Filter revisions"
                value={filter}
                onChange={(v) => { setFilter(v); setShowAll(false); }}
                options={[
                  { value: "needs-attention", label: "Needs attention", count: needsCount, tone: needsCount ? "warn" : undefined },
                  { value: "all", label: "All", count: rows.length },
                ]}
              />
            }
          />
        )}
        {rows === null && loadErr ? (
          <EmptyState
            icon={<AlertTriangle />}
            title="The history could not be loaded"
            hint={loadErr}
            action={<Button variant="secondary" onClick={() => void load()}><RefreshCw /> Try again</Button>}
          />
        ) : rows === null ? (
          <SkeletonRows rows={6} cols={5} />
        ) : rows.length === 0 ? (
          <EmptyState size="sm" icon={<History />} title="No revisions yet" hint="Every validate and apply of the network configuration is recorded here." />
        ) : filtered.length === 0 ? (
          <EmptyState
            size="sm"
            title="Nothing needs attention"
            hint="No revision is pending confirmation, and nothing failed or rolled back since the active configuration."
            action={<Button variant="secondary" size="sm" onClick={() => setFilter("all")}>Show all</Button>}
          />
        ) : (
          <>
            <Table label="Configuration revisions">
              <THead>
                <TR>
                  <TH numeric className="w-0">#</TH>
                  <TH>State</TH>
                  <TH className="hidden sm:table-cell">Summary</TH>
                  <TH className="hidden md:table-cell">Applied</TH>
                  <TH className="hidden 2xl:table-cell">Confirmed</TH>
                  <TH className="hidden lg:table-cell">Failure</TH>
                  <TH pinEnd><span className="sr-only">Actions</span></TH>
                </TR>
              </THead>
              <TBody>
                {visible.map((r) => (
                  <TR key={r.id} onClick={() => openDetail(r)} selected={open?.id === r.id}>
                    <TD numeric className="font-mono">#{r.seq}</TD>
                    <TD>
                      <RevisionStateBadge state={r.state} />
                      {r.failure_reason && (
                        <div className="mt-1 max-w-[16rem] break-words text-caption text-destructive lg:hidden">{r.failure_reason}</div>
                      )}
                    </TD>
                    <TD truncate className="hidden max-w-[14rem] sm:table-cell" title={r.summary || undefined}>{r.summary || "—"}</TD>
                    <TD nowrap muted className="hidden md:table-cell">{r.applied_at ? formatDate(r.applied_at) : "—"}</TD>
                    <TD nowrap muted className="hidden 2xl:table-cell">{r.confirmed_at ? formatDate(r.confirmed_at) : "—"}</TD>
                    <TD truncate className="hidden max-w-[12rem] text-caption text-destructive lg:table-cell" title={r.failure_reason ?? ""}>
                      {r.failure_reason || <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD pinEnd className="w-0" onClick={(e) => e.stopPropagation()}>
                      <Button size="icon-xs" variant="ghost" onClick={() => openDetail(r)} aria-label={`Details for revision ${r.seq}`}>
                        <ChevronRight className="rtl:rotate-180" />
                      </Button>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
            {(hidden > 0 || (showAll && filtered.length > PREVIEW)) && (
              <CardFooter>
                {hidden > 0 ? (
                  <Button size="sm" variant="secondary" onClick={() => setShowAll(true)}>
                    Show {hidden} older revision{hidden === 1 ? "" : "s"}
                  </Button>
                ) : (
                  <Button size="sm" variant="ghost" onClick={() => setShowAll(false)}>Show fewer</Button>
                )}
              </CardFooter>
            )}
          </>
        )}
      </Card>

      <DetailsDrawer
        open={open !== null}
        onOpenChange={(v) => !v && setOpenSnap(null)}
        width="lg"
        eyebrow="Configuration revision"
        title={open ? `Revision #${open.seq}` : ""}
        description={open?.summary || undefined}
        badges={open ? <RevisionStateBadge state={open.state} /> : undefined}
        error={detailErr}
        loading={!!open && !d && !detailErr}
        actions={
          detailErr && open ? (
            <Button variant="secondary" onClick={() => void fetchDetail(open)}><RefreshCw /> Try again</Button>
          ) : writable && open?.state === "pending_confirmation" ? (
            <>
              <Button variant="secondary" disabled={busy !== null} onClick={() => onRollback(open.id)}>
                {busy?.id === open.id && busy.kind === "rollback" ? "Rolling back…" : "Roll back now"}
              </Button>
              <Button disabled={busy !== null} onClick={() => onConfirm(open.id)}>
                {busy?.id === open.id && busy.kind === "confirm" ? "Keeping…" : "Keep this change"}
              </Button>
            </>
          ) : undefined
        }
      >
        {open && (
          <div className="space-y-6">
            <DescriptionList
              items={[
                { label: "State", value: revisionStateLabel(open.state) },
                { label: "Created", value: open.created_at ? formatDate(open.created_at) : "—" },
                { label: "Applied", value: open.applied_at ? formatDate(open.applied_at) : "—" },
                { label: "Confirmed", value: open.confirmed_at ? formatDate(open.confirmed_at) : "—" },
                {
                  label: "Rolls back at",
                  value: open.confirm_deadline
                    ? <>{formatDate(open.confirm_deadline)}{openLeft !== null && <span className="tabular text-muted-foreground"> · in {clock(openLeft)}</span>}</>
                    : "—",
                  hidden: !(open.confirm_deadline && open.state === "pending_confirmation"),
                },
                {
                  label: "Failure",
                  value: <span className="text-destructive">{open.failure_reason}</span>,
                  hidden: !open.failure_reason,
                },
              ]}
            />
            {d && (
              <>
                <DetailsSection title="Validation">
                  {d.validation ? (
                    <div className="space-y-2">
                      <StatusBadge tone={d.validation.ok ? "ok" : "err"}>{d.validation.ok ? "Passed" : "Issues found"}</StatusBadge>
                      {d.validation.issues && <ValidationIssueList issues={d.validation.issues} />}
                    </div>
                  ) : <p className="text-sm text-muted-foreground">Not recorded.</p>}
                </DetailsSection>
                <DetailsSection title="Apply events">
                  <Timeline
                    emptyLabel="No apply events recorded."
                    items={(d.events ?? []).map((e, k) => ({
                      key: String(k),
                      tone: e.ok ? "ok" : "err",
                      title: (
                        <span className="inline-flex flex-wrap items-center gap-2">
                          <span>{PHASE_WORDS[e.phase] ?? e.phase}</span>
                          <StatusBadge tone={e.ok ? "ok" : "err"}>{e.ok ? "OK" : "Failed"}</StatusBadge>
                          {PHASE_WORDS[e.phase] && <span className="font-mono text-caption text-muted-foreground">{e.phase}</span>}
                        </span>
                      ),
                      when: e.at ? formatDate(e.at) : undefined,
                      body: e.detail != null ? <EventDetail detail={e.detail} /> : undefined,
                    }))}
                  />
                </DetailsSection>
                <DetailsSection title="Health checks">
                  {d.health && d.health.length > 0 ? (
                    <HealthCheckList
                      checks={d.health.map((h) => ({ name: h.check_name, ok: h.ok, detail: h.detail, at: h.at ? formatDate(h.at) : undefined }))}
                    />
                  ) : <p className="text-sm text-muted-foreground">No health checks recorded.</p>}
                </DetailsSection>
              </>
            )}
          </div>
        )}
      </DetailsDrawer>
      {stepUp.dialog}
    </PageShell>
  );
}
