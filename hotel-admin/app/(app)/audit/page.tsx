"use client";

import { useEffect, useState } from "react";
import { api, ListResp, AuditEntry } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { formatDate, errMsg } from "@/lib/utils";

function actionTone(action: string): "ok" | "info" | "err" | "warn" {
  const verb = action.includes(".") ? action.split(".").pop()! : action;
  if (verb === "created") return "ok";
  if (verb === "updated") return "info";
  if (verb === "deleted" || verb === "disabled" || verb === "removed" || verb.endsWith("_removed")) return "err";
  return "warn";
}

export default function AuditPage() {
  const [rows, setRows] = useState<AuditEntry[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [action, setAction] = useState("");
  const [limit, setLimit] = useState(100);

  async function load() {
    setRows(null);
    const q = new URLSearchParams();
    if (action.trim()) q.set("action", action.trim());
    q.set("limit", String(limit));
    try { setRows((await api.get<ListResp<AuditEntry>>(`/audit?${q.toString()}`)).data); }
    catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">System</div>
        <h1 className="text-2xl font-semibold">Audit log</h1>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <form
        onSubmit={(e) => { e.preventDefault(); load(); }}
        className="flex flex-wrap items-end gap-3 mb-4"
      >
        <div className="w-64">
          <Label>Action filter</Label>
          <Input value={action} onChange={(e) => setAction(e.target.value)} placeholder="e.g. operator.created" />
        </div>
        <div className="w-28">
          <Label>Limit</Label>
          <Input type="number" min={1} max={500} value={limit} onChange={(e) => setLimit(Number(e.target.value) || 100)} />
        </div>
        <Button type="submit">Apply</Button>
      </form>

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No audit entries" hint="Operator and system actions are recorded here." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Time</TH><TH>Actor</TH><TH>Action</TH><TH>Target</TH><TH>IP</TH><TH>Payload</TH></TR>
              </THead>
              <tbody>
                {rows.map((a, i) => (
                  <TR key={i}>
                    <TD className="text-muted whitespace-nowrap">{formatDate(a.ts)}</TD>
                    <TD>
                      <div className="text-xs">{a.actor_type}</div>
                      {a.actor_id && <div className="text-xs text-muted font-mono">{a.actor_id.slice(0, 8)}</div>}
                    </TD>
                    <TD><Badge tone={actionTone(a.action)}>{a.action}</Badge></TD>
                    <TD className="text-xs">
                      {a.target_type ?? "—"}
                      {a.target_id && <div className="text-muted font-mono">{a.target_id.length > 12 ? a.target_id.slice(0, 12) : a.target_id}</div>}
                    </TD>
                    <TD className="text-muted font-mono text-xs">{a.ip ?? "—"}</TD>
                    <TD>
                      {a.payload ? (
                        <pre className="text-[11px] text-muted max-w-sm overflow-x-auto">{JSON.stringify(a.payload, null, 2)}</pre>
                      ) : "—"}
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
