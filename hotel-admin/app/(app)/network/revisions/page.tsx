"use client";

import { useEffect, useState } from "react";
import {
  api, ListResp,
  NetRevision, NetRevisionDetail,
} from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageShell, PageHeader, Toolbar } from "@/components/ui/page";
import { FilterChips, KeyValueGrid, Timeline } from "@/components/ui/data";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { PendingChangeBanner, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { ChevronRight, History } from "lucide-react";
import { errMsg, formatDate } from "@/lib/utils";
import {
  HealthCheckList, REVISION_STATE, RevisionStateBadge, ValidationIssueList, useNetworkAccess,
} from "@/components/network/shared";

/** Presentation only: which states a person should look at. */
const NEEDS = new Set(["pending_confirmation", "rolled_back", "failed"]);
const PREVIEW = 15;

export default function RevisionsPage() {
  const [rows, setRows] = useState<NetRevision[] | null>(null);
  const { known, writable } = useNetworkAccess();
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<{ id: string; kind: "confirm" | "rollback" } | null>(null);
  const [open, setOpen] = useState<NetRevision | null>(null);
  const [detail, setDetail] = useState<Record<string, NetRevisionDetail>>({});
  const [detailErr, setDetailErr] = useState<string | null>(null);
  /** Clutter control. Nothing here removes a revision -- see the note below for why it must not. */
  const [filter, setFilter] = useState<"needs-attention" | "all">("all");
  const [showAll, setShowAll] = useState(false);
  const toast = useToast();

  const filtered = (rows ?? []).filter((r) => filter === "all" || NEEDS.has(r.state));
  const visible = showAll ? filtered : filtered.slice(0, PREVIEW);
  const needsCount = (rows ?? []).filter((r) => NEEDS.has(r.state)).length;
  const pending = (rows ?? []).find((r) => r.state === "pending_confirmation") ?? null;

  async function load() {
    try { setRows((await api.get<ListResp<NetRevision>>("/network/revisions")).data ?? []); }
    catch (e) { setErr(errMsg(e)); }
  }

  useEffect(() => {
    load();
  }, []);

  async function openDetail(r: NetRevision) {
    setOpen(r); setDetailErr(null);
    if (!detail[r.id]) {
      try {
        const d = await api.get<NetRevisionDetail>(`/network/revisions/${r.id}`);
        setDetail((m) => ({ ...m, [r.id]: d }));
      } catch (e) { setDetailErr(errMsg(e)); }
    }
  }

  async function onConfirm(id: string) {
    setBusy({ id, kind: "confirm" }); setErr(null);
    try { await api.post(`/network/revisions/${id}/confirm`); toast.success("Configuration confirmed"); load(); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(null); }
  }
  async function onRollback(id: string) {
    setBusy({ id, kind: "rollback" }); setErr(null);
    try { await api.post(`/network/revisions/${id}/rollback`); toast.success("Configuration rolled back"); load(); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(null); }
  }

  const d = open ? detail[open.id] : undefined;

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<History />}
        eyebrow="Networking"
        title="Config history"
        description="Every network validate and apply ever made, with what it checked and how it ended. Nothing here is ever deleted — rollback and the current configuration's history depend on it."
      />

      {known && !writable && <ReadOnlyNotice>Your role can view the history but not keep or roll back a change.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      {pending && (
        <PendingChangeBanner
          title={`Revision #${pending.seq} — confirm or it rolls back automatically`}
          description={pending.summary || undefined}
          deadline={pending.confirm_deadline ?? null}
          canAct={writable}
          busy={busy?.id === pending.id ? busy.kind : null}
          onConfirm={() => onConfirm(pending.id)}
          onRollback={() => onRollback(pending.id)}
        />
      )}

      {/* WHY THERE IS NO DELETE HERE.
          A revision is what rollback rolls back TO, and what the current configuration's provenance points
          at. Removing one does not tidy the list, it makes the remaining history misleading: a gap reads as
          "nothing happened" and a rollback target that no longer exists fails at the worst possible moment.
          So the clutter is handled by filtering and folding, and every revision stays. */}
      {rows !== null && rows.length > 0 && (
        <Toolbar className="items-center">
          <FilterChips
            label="Filter revisions"
            value={filter}
            onChange={(v) => { setFilter(v); setShowAll(false); }}
            options={[
              { value: "needs-attention", label: "Needs attention", count: needsCount, tone: needsCount ? "warn" : undefined },
              { value: "all", label: "All", count: rows.length },
            ]}
          />
          <span className="text-caption text-muted-foreground">
            {filter === "needs-attention"
              ? "Awaiting confirmation, rolled back or failed."
              : "Newest first. Select a row for its checks and events."}
          </span>
        </Toolbar>
      )}

      <Card>
        {rows === null ? (
          <SkeletonRows rows={6} cols={5} />
        ) : rows.length === 0 ? (
          <EmptyState icon={<History />} title="No revisions yet" hint="Every validate and apply of the network configuration is recorded here." />
        ) : filtered.length === 0 ? (
          <EmptyState
            title="Nothing needs attention"
            hint="No revision is awaiting confirmation, rolled back or failed."
            action={<Button variant="secondary" size="sm" onClick={() => setFilter("all")}>Show all</Button>}
          />
        ) : (
          <>
            <Table>
              <THead>
                <TR>
                  <TH>#</TH>
                  <TH>State</TH>
                  <TH className="hidden sm:table-cell">Summary</TH>
                  <TH className="hidden md:table-cell">Applied</TH>
                  <TH className="hidden lg:table-cell">Confirmed</TH>
                  <TH className="hidden lg:table-cell">Failure</TH>
                  <TH><span className="sr-only">Actions</span></TH>
                </TR>
              </THead>
              <TBody>
                {visible.map((r) => (
                  <TR key={r.id} className="cursor-pointer" onClick={() => openDetail(r)}>
                    <TD className="font-mono tabular">#{r.seq}</TD>
                    <TD><RevisionStateBadge state={r.state} /></TD>
                    <TD className="hidden max-w-sm sm:table-cell">{r.summary || "—"}</TD>
                    <TD className="hidden whitespace-nowrap text-muted-foreground md:table-cell">{r.applied_at ? formatDate(r.applied_at) : "—"}</TD>
                    <TD className="hidden whitespace-nowrap text-muted-foreground lg:table-cell">{r.confirmed_at ? formatDate(r.confirmed_at) : "—"}</TD>
                    <TD className="hidden max-w-xs truncate text-caption text-destructive lg:table-cell" title={r.failure_reason ?? ""}>
                      {r.failure_reason || <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD className="whitespace-nowrap text-end" onClick={(e) => e.stopPropagation()}>
                      <Button size="sm" variant="ghost" onClick={() => openDetail(r)} aria-label={`Details for revision ${r.seq}`}>
                        <span className="hidden sm:inline">Details</span> <ChevronRight className="rtl:rotate-180" />
                      </Button>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
            {filtered.length > visible.length && (
              <div className="border-t border-border p-3">
                <Button size="sm" variant="secondary" onClick={() => setShowAll(true)}>
                  Show {filtered.length - visible.length} older revision{filtered.length - visible.length === 1 ? "" : "s"}
                </Button>
              </div>
            )}
          </>
        )}
      </Card>

      <Sheet open={open !== null} onOpenChange={(v) => !v && setOpen(null)}>
        <SheetContent width="lg">
          {open && (
            <>
              <SheetHeader
                eyebrow="Config history"
                title={`Revision #${open.seq}`}
                description={open.summary || undefined}
                badges={<RevisionStateBadge state={open.state} />}
                icon={<History />}
              />
              <SheetBody>
                <ErrorBanner err={detailErr} className="mb-0" />
                <KeyValueGrid
                  items={[
                    { label: "State", value: REVISION_STATE[open.state]?.label ?? open.state },
                    { label: "Created", value: open.created_at ? formatDate(open.created_at) : "—" },
                    { label: "Applied", value: open.applied_at ? formatDate(open.applied_at) : "—" },
                    { label: "Confirmed", value: open.confirmed_at ? formatDate(open.confirmed_at) : "—" },
                    ...(open.confirm_deadline && open.state === "pending_confirmation"
                      ? [{ label: "Rolls back at", value: formatDate(open.confirm_deadline) }] : []),
                    ...(open.failure_reason ? [{ label: "Failure", value: <span className="text-destructive">{open.failure_reason}</span>, wide: true }] : []),
                  ]}
                />
                {!d && !detailErr ? (
                  <div className="space-y-3" aria-busy="true">
                    <span className="sr-only">Loading</span>
                    <Skeleton className="h-16 w-full" />
                    <Skeleton className="h-24 w-full" />
                  </div>
                ) : d ? (
                  <>
                    <SheetSection title="Validation">
                      {d.validation ? (
                        <div className="space-y-2">
                          <Badge tone={d.validation.ok ? "ok" : "err"}>{d.validation.ok ? "Passed" : "Issues found"}</Badge>
                          {d.validation.issues && <ValidationIssueList issues={d.validation.issues} />}
                        </div>
                      ) : <p className="text-sm text-muted-foreground">Not recorded.</p>}
                    </SheetSection>
                    <SheetSection title="Apply events">
                      <Timeline
                        emptyLabel="No apply events recorded."
                        items={(d.events ?? []).map((e, k) => ({
                          key: String(k),
                          tone: e.ok ? "ok" : "err",
                          title: (
                            <span className="inline-flex items-center gap-2">
                              <span className="font-mono text-xs">{e.phase}</span>
                              <Badge tone={e.ok ? "ok" : "err"}>{e.ok ? "OK" : "Failed"}</Badge>
                            </span>
                          ),
                          when: e.at ? formatDate(e.at) : undefined,
                          body: e.detail != null
                            ? <span className="break-words font-mono text-caption">{typeof e.detail === "string" ? e.detail : JSON.stringify(e.detail)}</span>
                            : undefined,
                        }))}
                      />
                    </SheetSection>
                    <SheetSection title="Health checks">
                      {d.health && d.health.length > 0 ? (
                        <HealthCheckList
                          checks={d.health.map((h) => ({ name: h.check_name, ok: h.ok, detail: h.detail, at: h.at ? formatDate(h.at) : undefined }))}
                        />
                      ) : <p className="text-sm text-muted-foreground">No health checks recorded.</p>}
                    </SheetSection>
                  </>
                ) : null}
              </SheetBody>
              {writable && open.state === "pending_confirmation" && (
                <SheetFooter>
                  <Button variant="secondary" disabled={busy !== null} onClick={() => { void onRollback(open.id); setOpen(null); }}>
                    Roll back now
                  </Button>
                  <Button disabled={busy !== null} onClick={() => { void onConfirm(open.id); setOpen(null); }}>
                    Keep this change
                  </Button>
                </SheetFooter>
              )}
            </>
          )}
        </SheetContent>
      </Sheet>
    </PageShell>
  );
}
