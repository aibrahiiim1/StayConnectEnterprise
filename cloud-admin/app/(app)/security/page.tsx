"use client";

import { useEffect, useMemo, useState } from "react";
import { ShieldAlert, ShieldCheck } from "lucide-react";
import { api } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { PageHeader, PageShell, StatCard, Toolbar } from "@/components/ui/page";
import { SearchInput } from "@/components/ui/data";
import { SkeletonRows, Switch } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { statusWord } from "@/lib/license-state";
import { formatDate, formatRelative } from "@/lib/utils";

type Alert = {
  id: string;
  appliance_id: string;
  serial: string;
  kind: string;
  detail: Record<string, unknown> | null;
  source_ip: string;
  resolved: boolean;
  status: string;
  at: string;
};

const statusTone = (s: string) =>
  s === "open" ? "err" :
  s === "investigating" ? "warn" :
  s === "acknowledged" ? "warn" :
  s === "resolved" ? "ok" :
  s === "false_positive" ? "default" : "default";

const KIND_WORDS: Record<string, string> = {
  identity_hardware_mismatch: "Identity / hardware mismatch",
  hardware_reused: "Hardware reused",
  wan_mac_mismatch: "WAN MAC mismatch",
};
const kindWord = (k: string) => KIND_WORDS[k] ?? statusWord(k);

// Security alerts are raised by the appliance-registration/clone-protection path: identity_hardware_mismatch (a
// known identity key seen from different hardware), hardware_reused (a new identity on an in-use serial),
// wan_mac_mismatch, etc. This screen lists them and lets an operator triage each through its lifecycle.
export default function SecurityPage() {
  const toast = useToast();
  const [rows, setRows] = useState<Alert[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [showResolved, setShowResolved] = useState(false);
  const [query, setQuery] = useState("");

  async function load() {
    try {
      const r = await api.get<{ data: Alert[] }>("/cloud/v1/appliances-admin/security-alerts");
      setRows(r.data ?? []);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, []);

  async function setStatus(a: Alert, status: string, reason = "") {
    setBusy(a.id); setErr(null);
    try {
      await api.patch(`/cloud/v1/appliances-admin/security-alerts/${a.id}`, { status, reason });
      toast.success(`Alert marked ${statusWord(status).toLowerCase()}`, a.serial || undefined);
      await load();
      return true;
    } catch (e: any) {
      setErr(e?.message ?? "Update failed");
      return false;
    } finally {
      setBusy(null);
    }
  }

  // Resolutions carry a reason into the immutable audit trail.
  const [resolveReq, setResolveReq] = useState<{ a: Alert; status: "resolved" | "false_positive" } | null>(null);
  const [dlgErr, setDlgErr] = useState<string | null>(null);

  async function onResolve({ reason }: { reason: string }) {
    const r = resolveReq; if (!r) return;
    setDlgErr(null);
    setBusy(r.a.id);
    try {
      await api.patch(`/cloud/v1/appliances-admin/security-alerts/${r.a.id}`, { status: r.status, reason });
      setResolveReq(null);
      toast.success(`Alert marked ${statusWord(r.status).toLowerCase()}`, r.a.serial || undefined);
      await load();
    } catch (e: any) { setDlgErr(e?.message ?? "Update failed"); }
    finally { setBusy(null); }
  }

  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const a of rows ?? []) c[a.status] = (c[a.status] ?? 0) + 1;
    return c;
  }, [rows]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (rows ?? [])
      .filter((a) => showResolved || !a.resolved)
      .filter((a) => !q || [a.serial, a.kind, kindWord(a.kind), a.source_ip, a.status].join(" ").toLowerCase().includes(q));
  }, [rows, showResolved, query]);

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Administration"
        title="Security alerts"
        icon={<ShieldAlert />}
        description="Raised when an appliance registration looks wrong — a cloned identity, a reused serial, or a WAN MAC that does not match the signed license. Activation is blocked while an alert is open."
      />

      <ErrorBanner err={err} />

      <section aria-label="Alert counts" className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard label="Open" value={rows ? counts.open ?? 0 : "—"} tone="err" icon={<ShieldAlert />} hint="Blocks activation" />
        <StatCard label="Investigating" value={rows ? counts.investigating ?? 0 : "—"} tone="warn" />
        <StatCard label="Acknowledged" value={rows ? counts.acknowledged ?? 0 : "—"} tone="warn" />
        <StatCard label="Resolved or false positive" value={rows ? (counts.resolved ?? 0) + (counts.false_positive ?? 0) : "—"} tone="ok" icon={<ShieldCheck />} />
      </section>

      <Card>
        <Toolbar className="border-b border-border px-4 py-3">
          <SearchInput value={query} onChange={setQuery} placeholder="Search serial, kind, IP" label="Search alerts" />
          <label className="flex items-center gap-2 text-sm">
            <Switch checked={showResolved} onCheckedChange={setShowResolved} label="Show resolved" />
            Show resolved
          </label>
        </Toolbar>
        {rows === null ? (
          <SkeletonRows rows={4} cols={7} />
        ) : visible.length === 0 ? (
          rows.length === 0 || (!query && !showResolved) ? (
            <EmptyState icon={<ShieldCheck />} title="No security alerts" hint="Nothing suspicious has been detected." />
          ) : (
            <EmptyState title="No alerts match" hint="Nothing matches the search."
              action={<Button variant="secondary" onClick={() => setQuery("")}>Clear search</Button>} />
          )
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>When</TH><TH>Kind</TH><TH>Serial</TH><TH className="hidden md:table-cell">Source IP</TH>
                <TH className="hidden lg:table-cell">Detail</TH><TH>Status</TH><TH><span className="sr-only">Triage</span></TH>
              </TR>
            </THead>
            <tbody>
              {visible.map((a) => (
                <TR key={a.id}>
                  <TD className="whitespace-nowrap text-muted-foreground" title={formatDate(a.at)}>{formatRelative(a.at)}</TD>
                  <TD className="font-medium">{kindWord(a.kind)}</TD>
                  <TD className="font-mono text-xs">{a.serial || "—"}</TD>
                  <TD className="hidden font-mono text-xs md:table-cell">{a.source_ip || "—"}</TD>
                  <TD className="hidden max-w-xs lg:table-cell">
                    {a.detail ? (
                      <details className="text-xs">
                        <summary className="cursor-pointer text-muted-foreground hover:text-foreground">Show detail</summary>
                        <code className="mt-1 block break-all rounded bg-surface p-2 text-muted-foreground">{JSON.stringify(a.detail)}</code>
                      </details>
                    ) : "—"}
                  </TD>
                  <TD><Badge tone={statusTone(a.status) as any} dot>{statusWord(a.status)}</Badge></TD>
                  <TD>
                    <div className="flex flex-wrap justify-end gap-1">
                      {a.status === "open" && (
                        <Button size="sm" variant="secondary" disabled={busy === a.id} onClick={() => setStatus(a, "investigating")}>Investigate</Button>
                      )}
                      {!a.resolved && (
                        <Button size="sm" variant="ghost" disabled={busy === a.id} onClick={() => setStatus(a, "acknowledged")}>Acknowledge</Button>
                      )}
                      {!a.resolved && (
                        <Button size="sm" variant="secondary" disabled={busy === a.id} onClick={() => { setDlgErr(null); setResolveReq({ a, status: "resolved" }); }}>Resolve</Button>
                      )}
                      {!a.resolved && (
                        <Button size="sm" variant="ghost" disabled={busy === a.id} onClick={() => { setDlgErr(null); setResolveReq({ a, status: "false_positive" }); }}>False positive</Button>
                      )}
                      {a.resolved && (
                        <Button size="sm" variant="ghost" disabled={busy === a.id} onClick={() => setStatus(a, "open")}>Reopen</Button>
                      )}
                    </div>
                  </TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      <ConfirmDialog
        open={!!resolveReq}
        onOpenChange={(v) => { if (!v) setResolveReq(null); }}
        title={resolveReq?.status === "false_positive" ? "Mark as false positive" : "Resolve alert"}
        description={resolveReq ? <>{kindWord(resolveReq.a.kind)}{resolveReq.a.serial ? <> on <span className="font-mono">{resolveReq.a.serial}</span></> : null}.</> : undefined}
        confirmLabel={resolveReq?.status === "false_positive" ? "Mark false positive" : "Resolve"}
        busy={!!resolveReq && busy === resolveReq.a.id}
        error={dlgErr}
        requireReason
        reasonLabel="Reason"
        reasonPlaceholder="What did you establish?"
        onConfirm={onResolve}
      />
    </PageShell>
  );
}
