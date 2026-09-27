"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ShieldCheck } from "lucide-react";
import { api, itemsOf, type Items } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { PageHeader, PageShell, Toolbar } from "@/components/ui/page";
import { FilterChips, SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { StateBadge } from "@/components/status-badge";
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

export default function SecurityAlertsPage() {
  const toast = useToast();
  const { can } = usePermissions();
  const canTriage = can["securityAlerts.triage"];
  const [rows, setRows] = useState<Alert[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [view, setView] = useState<View>("open");
  const [query, setQuery] = useState("");
  const [closeReq, setCloseReq] = useState<{ a: Alert; status: "resolved" | "false_positive" } | null>(null);
  const [dlgErr, setDlgErr] = useState<unknown>(null);

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

  return (
    <PageShell width="wide">
      <PageHeader
        title="Security alerts"
        description="Suspicious appliance registrations, such as known hardware returning with a new identity. Central refuses those registrations; triage records what was done about each."
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
                ]}
              />
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} />

      <Card>
        <Toolbar className="border-b border-border px-4 py-3">
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
          <SearchInput value={query} onChange={setQuery} placeholder="Search serial, kind, address" label="Search alerts" />
        </Toolbar>
        {rows === null ? (
          <SkeletonRows rows={4} cols={6} />
        ) : visible.length === 0 ? (
          query ? (
            <EmptyState title="No alerts match" action={<Button variant="secondary" onClick={() => setQuery("")}>Clear search</Button>} />
          ) : (
            <EmptyState icon={<ShieldCheck />} title={view === "open" ? "No open alerts" : "No alerts"} hint="Nothing suspicious has been detected." />
          )
        ) : (
          <Table aria-label="Security alerts">
            <THead>
              <TR>
                <TH>When</TH><TH>What</TH><TH>Appliance</TH><TH className="hidden md:table-cell">From address</TH>
                <TH className="hidden lg:table-cell">Details</TH><TH>Status</TH><TH><span className="sr-only">Triage</span></TH>
              </TR>
            </THead>
            <tbody>
              {visible.map((a) => (
                <TR key={a.id}>
                  <TD className="whitespace-nowrap text-muted-foreground"><time dateTime={a.at} title={formatDateTime(a.at)}>{ago(a.at)}</time></TD>
                  <TD className="font-medium">{kindWord(a.kind)}</TD>
                  <TD>
                    {a.appliance_id ? (
                      <Link href={`/appliances/${a.appliance_id}`} className="font-mono text-xs underline-offset-2 hover:underline">{a.serial ?? "Appliance"}</Link>
                    ) : <span className="font-mono text-xs">{a.serial || "—"}</span>}
                  </TD>
                  <TD className="hidden font-mono text-xs md:table-cell">{a.source_ip || "—"}</TD>
                  <TD className="hidden max-w-xs lg:table-cell">
                    {a.detail && Object.keys(a.detail).length > 0 ? (
                      <details className="text-xs">
                        <summary className="cursor-pointer text-muted-foreground hover:text-foreground">Show</summary>
                        <code className="mt-1 block break-all rounded bg-surface p-2 text-muted-foreground">{JSON.stringify(a.detail)}</code>
                      </details>
                    ) : "—"}
                  </TD>
                  <TD><StateBadge info={alertStatusInfo(a.status)} /></TD>
                  <TD>
                    {canTriage && (
                      <div className="flex flex-wrap justify-end gap-1">
                        {a.status === "open" && (
                          <Button size="sm" variant="secondary" disabled={busy === a.id} onClick={() => setStatus(a, "investigating")}>Investigate</Button>
                        )}
                        {!closed(a) && a.status !== "acknowledged" && (
                          <Button size="sm" variant="ghost" disabled={busy === a.id} onClick={() => setStatus(a, "acknowledged")}>Acknowledge</Button>
                        )}
                        {!closed(a) && (
                          <Button size="sm" variant="secondary" disabled={busy === a.id} onClick={() => { setDlgErr(null); setCloseReq({ a, status: "resolved" }); }}>Resolve</Button>
                        )}
                        {!closed(a) && (
                          <Button size="sm" variant="ghost" disabled={busy === a.id} onClick={() => { setDlgErr(null); setCloseReq({ a, status: "false_positive" }); }}>False positive</Button>
                        )}
                        {closed(a) && (
                          <Button size="sm" variant="ghost" disabled={busy === a.id} onClick={() => setStatus(a, "open")}>Reopen</Button>
                        )}
                      </div>
                    )}
                  </TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

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
