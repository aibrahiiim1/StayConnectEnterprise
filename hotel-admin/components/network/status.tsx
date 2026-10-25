"use client";

import { useState } from "react";
import { Eye, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import {
  CHECK_STATE_LABEL, DiagCheck, NetworkDiagnostics, NetworkStatusValue, STATUS_LABEL, STATUS_TONE, changeSummary, NetworkChange,
} from "@/lib/api/network-overview";
import { StatusBadge, Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SectionCard } from "@/components/ui/card";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { errMsg } from "@/lib/utils";

/** What the network IS, from what the appliance confirmed and observes, with the reason as its tooltip. */
export function NetworkStatusBadge({ status, reason }: { status: NetworkStatusValue; reason?: string }) {
  return (
    <StatusBadge tone={STATUS_TONE[status]} title={reason}>
      {STATUS_LABEL[status]}
    </StatusBadge>
  );
}

/** For a network with a saved change that is not running yet: which settings differ. */
export function PendingChangeNote({ changes }: { changes: NetworkChange[] }) {
  if (!changes.length) return null;
  return <div className="mt-1 text-caption text-muted-foreground">Not applied: {changeSummary(changes)}</div>;
}

const TONE: Record<DiagCheck["state"], "ok" | "err" | "neutral" | "info"> = { ok: "ok", fail: "err", unknown: "neutral", info: "info" };

function CheckTable({ checks, label }: { checks: DiagCheck[]; label: string }) {
  return (
    <Table label={label}>
      <THead>
        <TR>
          <TH>Check</TH>
          <TH>Result</TH>
          <TH>What was observed</TH>
        </TR>
      </THead>
      <TBody>
        {checks.map((c) => (
          <TR key={c.id}>
            <TD nowrap>{c.label}</TD>
            <TD nowrap><Badge tone={TONE[c.state]}>{CHECK_STATE_LABEL[c.state]}</Badge></TD>
            <TD className="text-muted-foreground">{c.detail || "—"}</TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );
}

/** Read-only checks of one client network. Nothing here changes anything, and what the appliance cannot see is listed as such. */
export function DiagnosticsPanel({ networkId }: { networkId: string }) {
  const [report, setReport] = useState<NetworkDiagnostics | null>(null);
  const [busy, setBusy] = useState<null | "check" | "watch">(null);
  const [err, setErr] = useState<string | null>(null);

  async function run(sampleMs: number) {
    setBusy(sampleMs > 0 ? "watch" : "check");
    setErr(null);
    try {
      const q = sampleMs > 0 ? `?sample_ms=${sampleMs}` : "";
      setReport(await api.get<NetworkDiagnostics>(`/network/guest-networks/${networkId}/diagnostics${q}`));
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <SectionCard
      title="Diagnostics"
      description="What this appliance observes about the network right now. Nothing here changes anything."
      actions={
        <>
          <Button variant="secondary" size="sm" disabled={busy !== null} onClick={() => void run(0)}>
            <RefreshCw /> {busy === "check" ? "Checking…" : report ? "Check again" : "Run checks"}
          </Button>
          <Button variant="secondary" size="sm" disabled={busy !== null} onClick={() => void run(5000)}>
            <Eye /> {busy === "watch" ? "Watching…" : "Watch frames for 5 seconds"}
          </Button>
        </>
      }
    >
      <ErrorBanner err={err} className="mb-3" />
      {!report && !err && (
        <p className="text-sm text-muted-foreground">
          Run the checks to see the port link, the VLAN interface, the gateway, DHCP and DNS as the appliance sees them.
        </p>
      )}
      {report?.not_running && (
        <Callout tone="info" title="Not part of the confirmed configuration yet">
          There is nothing to observe until this network has been applied and confirmed.
        </Callout>
      )}
      {report && !report.not_running && (
        <div className="space-y-4">
          {report.network.findings.length > 0 && (
            <Callout tone="warning" title="What the appliance found">
              <ul className="list-disc space-y-1 pl-5">
                {report.network.findings.map((f) => <li key={f}>{f}</li>)}
              </ul>
            </Callout>
          )}
          <CheckTable label="Network checks" checks={report.network.checks} />
          {report.network.outside.length > 0 && (
            <Callout tone="info" title="Check outside the appliance">
              <p className="mb-1">The appliance cannot see these. They are places to look, not findings.</p>
              <ul className="list-disc space-y-1 pl-5">
                {report.network.outside.map((o) => <li key={o}>{o}</li>)}
              </ul>
            </Callout>
          )}
          {report.server.length > 0 && <CheckTable label="Appliance checks" checks={report.server} />}
        </div>
      )}
    </SectionCard>
  );
}
