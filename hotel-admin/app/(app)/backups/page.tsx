"use client";

import { useEffect, useState } from "react";
import { api, ListResp, BackupRecord } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { formatBytes, formatDate, errMsg } from "@/lib/utils";

function statusTone(s: string): "ok" | "err" | "info" | "default" {
  switch (s) {
    case "ok":      return "ok";
    case "failed":  return "err";
    case "running": return "info";
    default:        return "default";
  }
}

export default function BackupsPage() {
  const [rows, setRows] = useState<BackupRecord[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    try { setRows((await api.get<ListResp<BackupRecord>>("/backups")).data); }
    catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { load(); }, []);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">System</div>
        <h1 className="text-2xl font-semibold">Backups</h1>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No backups yet" hint="Scheduled and manual backups will appear here." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Started</TH><TH>Finished</TH><TH>Status</TH><TH>Kind</TH><TH>Path</TH><TH className="text-right">Size</TH><TH>Error</TH></TR>
              </THead>
              <tbody>
                {rows.map((b) => (
                  <TR key={b.id}>
                    <TD className="text-muted">{formatDate(b.started_at)}</TD>
                    <TD className="text-muted">{b.finished_at ? formatDate(b.finished_at) : "—"}</TD>
                    <TD><Badge tone={statusTone(b.status)}>{b.status}</Badge></TD>
                    <TD className="font-mono text-xs">{b.kind}</TD>
                    <TD className="font-mono text-xs max-w-xs truncate" title={b.path ?? ""}>{b.path ?? "—"}</TD>
                    <TD className="text-right text-muted">{b.size_bytes != null ? formatBytes(b.size_bytes) : "—"}</TD>
                    <TD className="text-err text-xs max-w-xs truncate" title={b.error ?? ""}>{b.error ?? ""}</TD>
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
