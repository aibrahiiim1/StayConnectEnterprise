"use client";

import { useEffect, useState } from "react";
import { api, AuditEntry, ListResp } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Input, Label } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { errMsg, formatDate } from "@/lib/utils";

const ACTION_TONE: Record<string, "ok" | "warn" | "err" | "info" | "default"> = {
  created: "ok",
  updated: "info",
  deleted: "err",
  archived: "warn",
  revoked: "warn",
  disabled: "warn",
  password_reset: "warn",
  role_added: "info",
  role_removed: "warn",
  plan_changed: "info",
  disconnected: "warn",
};

function tone(action: string) {
  const verb = action.split(".")[1] ?? "";
  return ACTION_TONE[verb] ?? "default";
}

export default function AuditPage() {
  // The audit log is per-customer. Requires a concrete customer in the Global
  // Customer Context; "All Customers" shows a prompt.
  const { selectedTenantId: tenantID, selectedTenantName, ready } = useCustomer();
  const allCustomers = tenantID === "";
  const [rows, setRows] = useState<AuditEntry[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [actionFilter, setActionFilter] = useState("");

  async function load() {
    if (!ready) return;
    if (allCustomers) { setRows(null); return; }
    setRows(null);
    const q = new URLSearchParams();
    if (actionFilter) q.set("action", actionFilter);
    try {
      const r = await api.get<ListResp<AuditEntry>>(`/v1/tenants/${tenantID}/audit?${q.toString()}`);
      setRows(r.data);
    } catch (e) { setErr(e); }
  }
  useEffect(() => { load(); }, [ready, tenantID]);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">Compliance</div>
        <h1 className="text-2xl font-semibold">Audit log</h1>
        <div className="mt-1 text-sm text-muted">{allCustomers ? "All Customers" : <>Customer: <span className="text-text font-medium">{selectedTenantName}</span></>}</div>
      </div>

      <ErrorBanner err={err} />

      {allCustomers && (
        <Card><CardBody>
          <div className="text-sm text-muted">
            The audit log is per-customer. Select a customer in the <strong>Customer context</strong> selector
            {" "}(top-left) to view its audit events.
          </div>
        </CardBody></Card>
      )}

      {!allCustomers && (<>
      <Card className="mb-4">
        <CardBody>
          <form onSubmit={(e) => { e.preventDefault(); load(); }} className="flex items-end gap-3">
            <div className="flex-1 max-w-md">
              <Label>Filter actions (comma-separated, e.g. <span className="font-mono">site.created,operator.disabled</span>)</Label>
              <Input
                value={actionFilter}
                onChange={(e) => setActionFilter(e.target.value)}
                placeholder="leave blank for all"
              />
            </div>
            <Button type="submit">Apply</Button>
          </form>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{rows ? `${rows.length} events (last 7 days)` : "Loading…"}</CardTitle>
        </CardHeader>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No audit events in this window" />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>When</TH>
                  <TH>Actor</TH>
                  <TH>Action</TH>
                  <TH>Target</TH>
                  <TH>IP</TH>
                  <TH>Payload</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((e, i) => (
                  <TR key={i}>
                    <TD className="text-muted whitespace-nowrap">{formatDate(e.ts)}</TD>
                    <TD>
                      <div className="text-sm">{e.actor_type}</div>
                      {e.actor_id && <div className="text-xs text-muted font-mono">{e.actor_id.slice(0, 8)}</div>}
                    </TD>
                    <TD>
                      <Badge tone={tone(e.action)}>{e.action}</Badge>
                    </TD>
                    <TD>
                      {e.target_type ? (
                        <>
                          <div className="text-sm">{e.target_type}</div>
                          {e.target_id && <div className="text-xs text-muted font-mono">{e.target_id.slice(0, 8)}</div>}
                        </>
                      ) : "—"}
                    </TD>
                    <TD className="text-muted font-mono text-xs">{e.ip ?? "—"}</TD>
                    <TD>
                      <pre className="text-xs text-muted font-mono max-w-md overflow-x-auto">
                        {e.payload && Object.keys(e.payload).length > 0 ? JSON.stringify(e.payload) : "—"}
                      </pre>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
      </>)}
    </div>
  );
}
