"use client";

import { useEffect, useState } from "react";
import { AlertTriangle, HardDrive, History, ShieldCheck, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { RoleRestricted } from "@/components/role-restricted";
import { usePermissions } from "@/lib/permissions";
import { Meter, Skeleton } from "@/components/ui/misc";
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

function ItemTable({
  title, description, items, tone,
}: { title: string; description?: string; items?: string[]; tone: "ok" | "warn" | "err" | "default" }) {
  const rows = (items ?? []).map((s) => s.split("|"));
  return (
    <Card>
      <CardHeader>
        <div className="space-y-0.5">
          <CardTitle>
            {title} <span className="text-sm font-normal text-muted-foreground tabular">({rows.length})</span>
          </CardTitle>
          {description && <CardDescription>{description}</CardDescription>}
        </div>
      </CardHeader>
      {rows.length === 0 ? (
        <EmptyState title="None" className="py-8" />
      ) : (
        <Table>
          <THead><TR><TH>Type</TH><TH>Artifact</TH><TH className="hidden md:table-cell">Reason</TH></TR></THead>
          <tbody>
            {rows.map((r, i) => (
              <TR key={i}>
                <TD><Badge tone={tone}>{r[0]}</Badge></TD>
                <TD className="break-all font-mono text-[11px]">{r[1]}</TD>
                <TD className="hidden text-xs text-muted-foreground md:table-cell">{r[2]}</TD>
              </TR>
            ))}
          </tbody>
        </Table>
      )}
    </Card>
  );
}

/**
 * Backup / rollback retention health for Central's own host. Read-only view of what the backup cleanup tool
 * retained, pinned, protected and would delete, plus disk usage, last run, failures and whether a valid rollback
 * path exists.
 */
export default function BackupHealthPage() {
  // Reading needs "backupHealth.read" (lib/permissions.ts); the server refuses this page's list to other roles.
  const { can } = usePermissions();
  const canRead = can["backupHealth.read"];
  const [data, setData] = useState<Resp | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api.get<Resp>("/cloud/v1/backup-health").then(setData).catch((e) => setErr(e?.message ?? "Failed to load"));
  }, []);

  const s = data?.available ? data.status : undefined;

  return (
    <PageShell>
      <PageHeader
        eyebrow="Administration"
        title="Backup health"
        icon={<HardDrive />}
        description="Whether Central's own backup and rollback storage is healthy. The cleanup never deletes the current or previous release, certificate-authority material, the newest full database backup, or operator-pinned artifacts."
      />

      {!canRead ? (
        <RoleRestricted what="Backup health describes Central's own host." />
      ) : (
      <>
      <ErrorBanner err={err} />
      {data && !data.available && (
        <Callout tone="warning" title="Backup status unavailable">{data.message}</Callout>
      )}

      {!data && !err && (
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4" aria-busy="true">
          <span className="sr-only">Loading</span>
          {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-28" />)}
        </div>
      )}

      {s && (
        <>
          <section aria-label="Backup health figures" className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            <StatCard
              label="Disk used"
              icon={<HardDrive />}
              tone={diskTone(s.disk_alert)}
              value={`${s.disk_pct}%`}
              footer={<Meter value={s.disk_pct} max={100} tone={diskTone(s.disk_alert)} caption={`Warn ${s.disk_warn}% · critical ${s.disk_crit}%`} />}
            />
            <StatCard
              label="Rollback path"
              icon={s.rollback_path_valid ? <ShieldCheck /> : <AlertTriangle />}
              tone={s.rollback_path_valid ? "ok" : "err"}
              value={<Badge tone={s.rollback_path_valid ? "ok" : "err"} dot className="text-sm">{s.rollback_path_valid ? "Valid" : "Invalid"}</Badge>}
              hint="Current and previous release present"
            />
            <StatCard
              label="Last cleanup"
              icon={<History />}
              value={<span className="text-headline">{formatRelative(s.last_run)}</span>}
              hint={`Mode ${s.mode} · deleted ${s.deleted_last_run} · ${Math.round((s.reclaimed_kb || 0) / 1024)} MB freed`}
            />
            <StatCard
              label="Failures"
              icon={<AlertTriangle />}
              tone={s.failures ? "err" : "ok"}
              value={s.failures}
              hint={s.failure_detail || "None"}
            />
          </section>

          <Callout tone="neutral" title="Retention policy">
            Keep the newest {s.keep_binaries} binaries · {s.keep_releases} releases (plus current and previous) ·{" "}
            {s.keep_db} database dumps (the newest is never deleted) · {s.keep_config} config backups. Retained{" "}
            <strong>{s.retained}</strong> · pinned <strong>{s.pinned}</strong> · protected <strong>{s.protected}</strong> ·
            would delete <strong>{s.delete_candidates}</strong>.
          </Callout>

          <ItemTable title="Protected" description="Never deleted." items={s.protected_items} tone="ok" />
          {(s.pinned ?? 0) > 0 && <ItemTable title="Operator-pinned" items={s.pinned_items} tone="warn" />}
          <ItemTable title="Retained" items={s.retained_items} tone="default" />
          <ItemTable title="Delete candidates" description="Removed on the next cleanup run." items={s.delete_items} tone="err" />
        </>
      )}
      {s === undefined && data?.available && (
        <EmptyState icon={<Trash2 />} title="No status reported" />
      )}
      </>
      )}
    </PageShell>
  );
}
