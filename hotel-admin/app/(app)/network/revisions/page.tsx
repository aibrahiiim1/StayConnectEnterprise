"use client";

import { useEffect, useState } from "react";
import {
  api, ListResp,
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
import { PendingChangeBanner, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { ChevronRight, History } from "lucide-react";
import { errMsg, formatDate } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";
import {
  HealthCheckList, RevisionStateBadge, ValidationIssueList, revisionStateLabel, useNetworkAccess,
} from "@/components/network/shared";
import { isReauth, useNetworkChangeStepUp } from "@/components/network/change-step-up";

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

  const stepUp = useNetworkChangeStepUp();

  function onConfirm(id: string) {
    stepUp.ask("confirm", async (password) => {
      setBusy({ id, kind: "confirm" }); setErr(null);
      try { await api.post(`/network/revisions/${id}/confirm`, { password }); toast.success("Configuration confirmed"); load(); }
      catch (e) { if (isReauth(e)) throw e; setErr(errMsg(e)); }
      finally { setBusy(null); }
    });
  }
  function onRollback(id: string) {
    stepUp.ask("rollback", async (password) => {
      setBusy({ id, kind: "rollback" }); setErr(null);
      try { await api.post(`/network/revisions/${id}/rollback`, { password }); toast.success("Configuration rolled back"); load(); }
      catch (e) { if (isReauth(e)) throw e; setErr(errMsg(e)); }
      finally { setBusy(null); }
    });
  }

  const d = open ? detail[open.id] : undefined;
  const hidden = filtered.length - visible.length;

  return (
    <PageShell width="wide">
      <PageHeader
        title="Config history"
        description="Every network validate and apply, with what it checked and how it ended."
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
          description={pending.summary || undefined}
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
        {rows === null ? (
          <SkeletonRows rows={6} cols={5} />
        ) : rows.length === 0 ? (
          <EmptyState size="sm" icon={<History />} title="No revisions yet" hint="Every validate and apply of the network configuration is recorded here." />
        ) : filtered.length === 0 ? (
          <EmptyState
            size="sm"
            title="Nothing needs attention"
            hint="No revision is pending confirmation, rolled back or failed."
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
                    <TD nowrap><RevisionStateBadge state={r.state} /></TD>
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
            {hidden > 0 && (
              <CardFooter>
                <Button size="sm" variant="secondary" onClick={() => setShowAll(true)}>
                  Show {hidden} older revision{hidden === 1 ? "" : "s"}
                </Button>
              </CardFooter>
            )}
          </>
        )}
      </Card>

      <DetailsDrawer
        open={open !== null}
        onOpenChange={(v) => !v && setOpen(null)}
        width="lg"
        eyebrow="Configuration revision"
        title={open ? `Revision #${open.seq}` : ""}
        description={open?.summary || undefined}
        badges={open ? <RevisionStateBadge state={open.state} /> : undefined}
        error={detailErr}
        loading={!!open && !d && !detailErr}
        actions={writable && open?.state === "pending_confirmation" ? (
          <>
            <Button variant="secondary" disabled={busy !== null} onClick={() => { void onRollback(open.id); setOpen(null); }}>
              Roll back now
            </Button>
            <Button disabled={busy !== null} onClick={() => { void onConfirm(open.id); setOpen(null); }}>
              Keep this change
            </Button>
          </>
        ) : undefined}
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
                  value: open.confirm_deadline ? formatDate(open.confirm_deadline) : "—",
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
                        <span className="inline-flex items-center gap-2">
                          <span className="font-mono text-xs">{e.phase}</span>
                          <StatusBadge tone={e.ok ? "ok" : "err"}>{e.ok ? "OK" : "Failed"}</StatusBadge>
                        </span>
                      ),
                      when: e.at ? formatDate(e.at) : undefined,
                      body: e.detail != null
                        ? <span className="break-words font-mono text-caption">{typeof e.detail === "string" ? e.detail : JSON.stringify(e.detail)}</span>
                        : undefined,
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
