"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ShieldCheck } from "lucide-react";
import { api, itemsOf, type Items } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { PageHeader, PageShell } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { DescriptionList, FilterChips, SearchInput } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { RowActions, type RowAction } from "@/components/ui/row-actions";
import { useToast } from "@/components/ui/toast";
import { StateBadge } from "@/components/status-badge";
import { ListState, PayloadView, hasPayload } from "@/components/record-view";
import { HelpList, HelpSection } from "@/components/help";
import { usePermissions } from "@/lib/permissions";
import { ago, alertStatusInfo, formatDateTime, sentenceCase } from "@/lib/status";

type Alert = {
  id: string;
  appliance_id?: string | null;
  serial?: string | null;
  kind: string;
  detail?: Record<string, unknown> | null;
  source_ip?: string | null;
  resolved?: boolean;
  status: string;
  at: string;
};

const KIND_WORDS: Record<string, string> = {
  identity_hardware_mismatch: "Known appliance on different hardware",
  hardware_reused: "Hardware already in use",
  wan_mac_mismatch: "WAN MAC does not match its license",
};
const kindWord = (k: string) => KIND_WORDS[k] ?? sentenceCase(k);
const closed = (a: Alert) => a.resolved ?? (a.status === "resolved" || a.status === "false_positive");

type View = "open" | "closed" | "all";
type CloseStatus = "resolved" | "false_positive";

export default function SecurityAlertsPage() {
  const toast = useToast();
  const { can } = usePermissions();
  const canTriage = can["securityAlerts.triage"];
  const [rows, setRows] = useState<Alert[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [view, setView] = useState<View>("open");
  const [query, setQuery] = useState("");
  const [closeReq, setCloseReq] = useState<{ a: Alert; status: CloseStatus } | null>(null);
  const [dlgErr, setDlgErr] = useState<unknown>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(itemsOf(await api.get<Items<Alert>>("/cloud/v1/security-alerts")));
      setErr(null);
    } catch (e) {
      setErr(e);
      setRows((p) => p ?? []);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function setStatus(a: Alert, status: string, reason = "") {
    setBusy(a.id);
    setErr(null);
    try {
      await api.patch(`/cloud/v1/security-alerts/${a.id}`, { status, reason });
      toast.success(`Alert marked ${alertStatusInfo(status).label.toLowerCase()}`, a.serial ?? undefined);
      await load();
      return true;
    } catch (e) {
      setErr(e);
      return false;
    } finally {
      setBusy(null);
    }
  }

  async function onClose({ reason }: { reason: string }) {
    const r = closeReq;
    if (!r) return;
    setDlgErr(null);
    setBusy(r.a.id);
    try {
      await api.patch(`/cloud/v1/security-alerts/${r.a.id}`, { status: r.status, reason });
      setCloseReq(null);
      toast.success(`Alert marked ${alertStatusInfo(r.status).label.toLowerCase()}`, r.a.serial ?? undefined);
      await load();
    } catch (e) {
      setDlgErr(e);
    } finally {
      setBusy(null);
    }
  }

  const askClose = (a: Alert, status: CloseStatus) => {
    setSelectedId(null);
    setDlgErr(null);
    setCloseReq({ a, status });
  };

  /** Every triage step for one alert, in the order an operator takes them. */
  const triage = (a: Alert): RowAction[] => [
    { label: "Investigate", hidden: a.status !== "open", disabled: busy === a.id, onSelect: () => void setStatus(a, "investigating") },
    { label: "Acknowledge", hidden: closed(a) || a.status === "acknowledged", disabled: busy === a.id, onSelect: () => void setStatus(a, "acknowledged") },
    { label: "Resolve", hidden: closed(a), disabled: busy === a.id, onSelect: () => askClose(a, "resolved") },
    { label: "False positive", hidden: closed(a), disabled: busy === a.id, onSelect: () => askClose(a, "false_positive") },
    { label: "Reopen", hidden: !closed(a), disabled: busy === a.id, onSelect: () => void setStatus(a, "open") },
  ];

  const counts = useMemo(() => {
    const all = rows ?? [];
    return { open: all.filter((a) => !closed(a)).length, closed: all.filter(closed).length, all: all.length };
  }, [rows]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (rows ?? [])
      .filter((a) => view === "all" || (view === "open" ? !closed(a) : closed(a)))
      .filter((a) => !q || [a.serial, a.kind, kindWord(a.kind), a.source_ip, a.status].join(" ").toLowerCase().includes(q));
  }, [rows, view, query]);

  const selected = rows?.find((a) => a.id === selectedId) ?? null;

  return (
    <PageShell width="wide">
      <PageHeader
        title="Security alerts"
        description="Suspicious appliance registrations, which Central refuses. Triage records what was done about each."
        help={
          <>
            <HelpSection title="When an alert is raised">
              <HelpList
                items={[
                  <><strong>Known appliance on different hardware</strong>: an appliance&apos;s identity was seen from other hardware, for example a cloned disk.</>,
                  <><strong>Hardware already in use</strong>: a new appliance registered with a serial that is already active.</>,
                  <><strong>WAN MAC does not match its license</strong>: the network card changed. If that was intended, rebind the WAN MAC on the appliance&apos;s page.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Triage">
              <HelpList
                items={[
                  <><strong>Investigate</strong> and <strong>Acknowledge</strong> record that someone is looking at it.</>,
                  <><strong>Resolve</strong> or <strong>False positive</strong> close it, with a reason for the audit log.</>,
                  <><strong>Reopen</strong> returns a closed alert to Open.</>,
                  <>Select an alert to see everything recorded about it.</>,
                ]}
              />
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} />

      <Card className="overflow-hidden">
        <FilterBar
          inset
          filters={
            <FilterChips<View>
              label="Show"
              value={view}
              onChange={setView}
              options={[
                { value: "open", label: "Open", count: rows ? counts.open : undefined, tone: counts.open ? "err" : undefined },
                { value: "closed", label: "Closed", count: rows ? counts.closed : undefined },
                { value: "all", label: "All", count: rows ? counts.all : undefined },
              ]}
            />
          }
          search={<SearchInput value={query} onChange={setQuery} placeholder="Search serial, kind, address" label="Search alerts" />}
        />
        <ListState
          rows={rows === null ? null : visible}
          cols={6}
          filtered={!!query}
          noMatch={<EmptyState size="sm" title="No alerts match" action={<Button variant="secondary" size="sm" onClick={() => setQuery("")}>Clear search</Button>} />}
          empty={<EmptyState size="sm" icon={<ShieldCheck />} title={view === "open" ? "No open alerts" : "No alerts"} hint="Nothing suspicious has been detected." />}
        >
          <Table aria-label="Security alerts">
            <THead>
              <TR>
                <TH>When</TH>
                <TH>What</TH>
                <TH>Appliance</TH>
                <TH className="hidden md:table-cell">From address</TH>
                <TH>Status</TH>
                <TH className="w-0"><span className="sr-only">Triage</span></TH>
              </TR>
            </THead>
            <TBody>
              {visible.map((a) => {
                const steps = triage(a);
                const first = steps.find((s) => !s.hidden);
                return (
                  <TR key={a.id} onClick={() => setSelectedId(a.id)} selected={a.id === selectedId}>
                    <TD nowrap muted><time dateTime={a.at} title={formatDateTime(a.at)}>{ago(a.at)}</time></TD>
                    <TD className="font-medium">{kindWord(a.kind)}</TD>
                    <TD nowrap>
                      {a.appliance_id ? (
                        <Link
                          href={`/appliances/${a.appliance_id}`}
                          onClick={(e) => e.stopPropagation()}
                          className="rounded font-mono text-xs underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          {a.serial ?? "Appliance"}
                        </Link>
                      ) : <span className="font-mono text-xs">{a.serial || "—"}</span>}
                    </TD>
                    <TD nowrap className="hidden font-mono text-xs md:table-cell">{a.source_ip || "—"}</TD>
                    <TD nowrap><StateBadge info={alertStatusInfo(a.status)} /></TD>
                    <TD className="w-0">
                      {canTriage && (
                        <RowActions
                          subject={`${kindWord(a.kind)}${a.serial ? ` on ${a.serial}` : ""}`}
                          primary={first}
                          items={steps.filter((s) => s !== first)}
                        />
                      )}
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        </ListState>
      </Card>

      <DetailsDrawer
        open={!!selected}
        onOpenChange={(v) => { if (!v) setSelectedId(null); }}
        eyebrow="Security alert"
        title={selected ? kindWord(selected.kind) : ""}
        description={selected ? formatDateTime(selected.at) : undefined}
        badges={selected ? <StateBadge info={alertStatusInfo(selected.status)} /> : undefined}
        actions={
          selected && canTriage ? (
            triage(selected).filter((s) => !s.hidden).map((s) => (
              <Button key={s.label} size="sm" variant="secondary" disabled={s.disabled} onClick={s.onSelect}>
                {s.label}
              </Button>
            ))
          ) : undefined
        }
      >
        {selected && (
          <>
            <DetailsSection title="Alert">
              <DescriptionList
                items={[
                  {
                    label: "Appliance",
                    value: selected.appliance_id ? (
                      <Link href={`/appliances/${selected.appliance_id}`} className="font-mono text-xs text-primary underline-offset-2 hover:underline">
                        {selected.serial ?? "Appliance"}
                      </Link>
                    ) : selected.serial ? <span className="font-mono text-xs">{selected.serial}</span> : null,
                  },
                  { label: "From address", value: selected.source_ip ? <span className="font-mono text-xs">{selected.source_ip}</span> : null },
                  { label: "Raised", value: formatDateTime(selected.at) },
                  { label: "Kind", value: <span className="font-mono text-xs">{selected.kind}</span> },
                ]}
              />
            </DetailsSection>
            {hasPayload(selected.detail) && (
              <DetailsSection title="What Central recorded">
                <PayloadView payload={selected.detail} />
              </DetailsSection>
            )}
          </>
        )}
      </DetailsDrawer>

      <ConfirmDialog
        open={!!closeReq}
        onOpenChange={(v) => { if (!v) setCloseReq(null); }}
        title={closeReq?.status === "false_positive" ? "Mark as false positive" : "Resolve alert"}
        description={closeReq ? <>{kindWord(closeReq.a.kind)}{closeReq.a.serial ? <> on <span className="font-mono">{closeReq.a.serial}</span></> : null}.</> : undefined}
        confirmLabel={closeReq?.status === "false_positive" ? "Mark false positive" : "Resolve"}
        busy={!!closeReq && busy === closeReq.a.id}
        error={dlgErr}
        requireReason
        reasonPlaceholder="What did you establish?"
        onConfirm={onClose}
      />
    </PageShell>
  );
}
