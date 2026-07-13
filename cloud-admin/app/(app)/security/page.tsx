"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative } from "@/lib/utils";

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

// Security alerts are raised by the appliance-registration/clone-protection path:
// identity_hardware_mismatch (a known identity key seen from different hardware),
// hardware_reused (a new identity on an in-use serial), wan_mac_mismatch, etc.
// This screen lists them and lets an operator triage each through its lifecycle.
export default function SecurityPage() {
  const [rows, setRows] = useState<Alert[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [showResolved, setShowResolved] = useState(false);

  async function load() {
    try {
      const r = await api.get<{ data: Alert[] }>("/cloud/v1/appliances-admin/security-alerts");
      setRows(r.data ?? []);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, []);

  async function setStatus(a: Alert, status: string) {
    // Resolutions carry a reason into the immutable audit trail.
    let reason = "";
    if (status === "resolved" || status === "false_positive") {
      reason = window.prompt(`Reason for marking this alert "${status.replace("_", " ")}":`, "") ?? "";
    }
    setBusy(a.id); setErr(null);
    try {
      await api.patch(`/cloud/v1/appliances-admin/security-alerts/${a.id}`, { status, reason });
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "Update failed");
    } finally {
      setBusy(null);
    }
  }

  const visible = (rows ?? []).filter((a) => showResolved || !a.resolved);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Administration</div>
          <h1 className="text-2xl font-semibold">Security alerts</h1>
        </div>
        <label className="text-sm text-muted flex items-center gap-2">
          <input type="checkbox" checked={showResolved} onChange={(e) => setShowResolved(e.target.checked)} />
          Show resolved
        </label>
      </div>

      <p className="text-sm text-muted mb-4">
        Raised when an appliance registration looks wrong — a cloned identity, a reused serial, or a
        WAN MAC that doesn&apos;t match the signed license. Auto-licensing is denied while an alert is open.
      </p>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <EmptyState title="Loading…" />
          ) : visible.length === 0 ? (
            <EmptyState title="No security alerts" hint="Nothing suspicious has been detected." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>When</TH><TH>Kind</TH><TH>Serial</TH><TH>Source IP</TH>
                  <TH>Detail</TH><TH>Status</TH><TH className="text-right">Triage</TH>
                </TR>
              </THead>
              <tbody>
                {visible.map((a) => (
                  <TR key={a.id}>
                    <TD className="text-muted whitespace-nowrap">{formatRelative(a.at)}</TD>
                    <TD className="font-mono text-xs">{a.kind}</TD>
                    <TD className="font-mono text-xs">{a.serial || "—"}</TD>
                    <TD className="font-mono text-xs">{a.source_ip || "—"}</TD>
                    <TD className="text-xs max-w-xs">
                      <code className="text-muted break-all">{a.detail ? JSON.stringify(a.detail) : "—"}</code>
                    </TD>
                    <TD><Badge tone={statusTone(a.status) as any}>{a.status}</Badge></TD>
                    <TD>
                      <div className="flex gap-1 justify-end flex-wrap">
                        {a.status === "open" && (
                          <Button size="sm" variant="secondary" disabled={busy === a.id} onClick={() => setStatus(a, "investigating")}>Investigate</Button>
                        )}
                        {!a.resolved && (
                          <Button size="sm" variant="ghost" disabled={busy === a.id} onClick={() => setStatus(a, "acknowledged")}>Acknowledge</Button>
                        )}
                        {!a.resolved && (
                          <Button size="sm" variant="secondary" disabled={busy === a.id} onClick={() => setStatus(a, "resolved")}>Resolve</Button>
                        )}
                        {!a.resolved && (
                          <Button size="sm" variant="ghost" disabled={busy === a.id} onClick={() => setStatus(a, "false_positive")}>False positive</Button>
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
        </CardBody>
      </Card>
    </div>
  );
}
