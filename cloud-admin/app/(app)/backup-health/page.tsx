"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative } from "@/lib/utils";

type Status = {
  host_role: string; last_run: string; mode: string;
  disk_pct: number; disk_alert: string; disk_warn: number; disk_crit: number;
  rollback_path_valid: boolean; failures: number; failure_detail: string;
  retained: number; pinned: number; protected: number; delete_candidates: number;
  deleted_last_run: number; reclaimed_kb: number;
  keep_binaries: number; keep_releases: number; keep_db: number; keep_config: number;
  protected_items?: string[]; pinned_items?: string[]; retained_items?: string[]; delete_items?: string[];
};
type Resp = { available: boolean; message?: string; status?: Status; pins?: string[] };

const diskTone = (a: string) => (a === "critical" ? "err" : a === "warning" ? "warn" : "ok");

function ItemTable({ title, items, tone }: { title: string; items?: string[]; tone: string }) {
  const rows = (items ?? []).map((s) => s.split("|"));
  return (
    <Card className="mb-4">
      <CardHeader><CardTitle>{title} <span className="text-muted text-sm font-normal">({rows.length})</span></CardTitle></CardHeader>
      <CardBody className="p-0 overflow-x-auto">
        {rows.length === 0 ? <EmptyState title="None" /> : (
          <Table>
            <THead><TR><TH>Type</TH><TH>Artifact</TH><TH>Reason</TH></TR></THead>
            <tbody>
              {rows.map((r, i) => (
                <TR key={i}>
                  <TD><Badge tone={tone as any}>{r[0]}</Badge></TD>
                  <TD className="font-mono text-[11px] break-all">{r[1]}</TD>
                  <TD className="text-muted text-xs">{r[2]}</TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}
      </CardBody>
    </Card>
  );
}

/**
 * Backup / rollback retention health for the Central host. Read-only view of what
 * the stayconnect-backup-cleanup tool retained, pinned, protected and would delete,
 * plus disk usage/alert, last run, failures and whether a valid rollback path exists.
 */
export default function BackupHealthPage() {
  const [data, setData] = useState<Resp | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api.get<Resp>("/cloud/v1/backup-health").then(setData).catch((e) => setErr(e?.message ?? "Failed to load"));
  }, []);

  return (
    <div className="p-6 max-w-[90rem] mx-auto">
      <div className="mb-1 text-xs text-muted uppercase tracking-wider">Administration · Operations</div>
      <h1 className="text-2xl font-semibold mb-1">Backup &amp; rollback health</h1>
      <p className="text-sm text-muted mb-4">
        Retention status for deployment/rollback artifacts on Central. The cleanup tool never deletes the current or
        previous release, PKI/custody material, the newest full DB backup, or operator-pinned artifacts.
      </p>

      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {data && !data.available && <div className="text-warn text-sm mb-4">{data.message}</div>}

      {data?.available && data.status && (() => {
        const s = data.status;
        return (
          <>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-5">
              <Card><CardBody>
                <div className="text-xs text-muted uppercase">Disk used</div>
                <div className="text-2xl font-semibold"><Badge tone={diskTone(s.disk_alert) as any}>{s.disk_pct}%</Badge></div>
                <div className="text-xs text-muted mt-1">warn {s.disk_warn}% · crit {s.disk_crit}%</div>
              </CardBody></Card>
              <Card><CardBody>
                <div className="text-xs text-muted uppercase">Rollback path</div>
                <div className="text-2xl font-semibold"><Badge tone={s.rollback_path_valid ? "ok" : "err"}>{s.rollback_path_valid ? "valid" : "INVALID"}</Badge></div>
                <div className="text-xs text-muted mt-1">current + previous present</div>
              </CardBody></Card>
              <Card><CardBody>
                <div className="text-xs text-muted uppercase">Last cleanup</div>
                <div className="text-lg font-semibold">{formatRelative(s.last_run)}</div>
                <div className="text-xs text-muted mt-1">mode {s.mode} · deleted {s.deleted_last_run} · {Math.round((s.reclaimed_kb || 0) / 1024)}MB freed</div>
              </CardBody></Card>
              <Card><CardBody>
                <div className="text-xs text-muted uppercase">Failures</div>
                <div className="text-2xl font-semibold"><Badge tone={s.failures ? "err" : "ok"}>{s.failures}</Badge></div>
                <div className="text-xs text-muted mt-1 break-words">{s.failure_detail || "none"}</div>
              </CardBody></Card>
            </div>

            <div className="text-sm text-muted mb-4">
              Policy: keep newest {s.keep_binaries} binaries · {s.keep_releases} releases (+ current & previous) ·
              {" "}{s.keep_db} DB dumps (newest never deleted) · {s.keep_config} config backups.
              {" "}Retained <strong>{s.retained}</strong> · pinned <strong>{s.pinned}</strong> ·
              {" "}protected <strong>{s.protected}</strong> · would delete <strong>{s.delete_candidates}</strong>.
            </div>

            <ItemTable title="Protected (never deleted)" items={s.protected_items} tone="ok" />
            {(s.pinned ?? 0) > 0 && <ItemTable title="Operator-pinned" items={s.pinned_items} tone="warn" />}
            <ItemTable title="Retained" items={s.retained_items} tone="default" />
            <ItemTable title="Delete candidates (next apply)" items={s.delete_items} tone="err" />
          </>
        );
      })()}
    </div>
  );
}
