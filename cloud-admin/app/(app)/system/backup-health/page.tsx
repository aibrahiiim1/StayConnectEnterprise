"use client";

import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, Archive, HardDrive, History, ShieldCheck } from "lucide-react";
import { api } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { FilterChips } from "@/components/ui/data";
import { HelpList, HelpSection } from "@/components/help";
import { Meter, Skeleton } from "@/components/ui/misc";
import { ago, formatDateTime } from "@/lib/status";

type Status = {
  host_role: string; last_run: string; mode: string;
  disk_pct: number; disk_alert: string; disk_warn: number; disk_crit: number;
  rollback_path_valid: boolean; failures: number; failure_detail: string;
  retained: number; pinned: number; protected: number; delete_candidates: number;
  deleted_last_run: number; reclaimed_kb: number;
  keep_binaries: number; keep_releases: number; keep_db: number; keep_config: number;
  protected_items?: string[]; pinned_items?: string[]; retained_items?: string[]; delete_items?: string[];
};
type DatabaseBackup = {
  newest_file?: string | null; newest_at?: string | null; newest_bytes?: number;
  last_attempt_at?: string | null; last_success_at?: string | null; last_failure_at?: string | null;
  last_error?: string | null; healthy: boolean; problem?: string | null;
};
type Resp = { available: boolean; message?: string; status?: Status; pins?: string[]; database_backup?: DatabaseBackup };

type Group = "protected" | "pinned" | "retained" | "delete";

const GROUPS: { value: Group; label: string; tone: "ok" | "warn" | "default" | "err"; hint: string }[] = [
  { value: "protected", label: "Protected", tone: "ok", hint: "Never deleted." },
  { value: "pinned", label: "Pinned", tone: "warn", hint: "Kept because an operator pinned them." },
  { value: "retained", label: "Retained", tone: "default", hint: "Kept by the retention policy." },
  { value: "delete", label: "Delete candidates", tone: "err", hint: "Removed on the next cleanup run." },
];

const diskTone = (a: string) => (a === "critical" ? "err" : a === "warning" ? "warn" : "ok");

function itemsOf(s: Status, g: Group): string[][] {
  const raw = { protected: s.protected_items, pinned: s.pinned_items, retained: s.retained_items, delete: s.delete_items }[g];
  return (raw ?? []).map((x) => x.split("|"));
}

export default function BackupHealthPage() {
  const [data, setData] = useState<Resp | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [group, setGroup] = useState<Group>("protected");

  useEffect(() => {
    api.get<Resp>("/cloud/v1/backup-health").then(setData).catch((e) => setErr(e?.message ?? "Failed to load"));
  }, []);

  const s = data?.available ? data.status : undefined;
  const db = data?.database_backup;
  const lastFailed = !!db?.last_failure_at && (!db.last_success_at || db.last_failure_at > db.last_success_at);
  const rows = useMemo(() => (s ? itemsOf(s, group) : []), [s, group]);
  const current = GROUPS.find((g) => g.value === group)!;

  return (
    <PageShell>
      <PageHeader
        title="Backup health"
        description="Whether Central's own backups and rollback storage are healthy."
        help={
          <>
            <HelpSection title="What the cleanup never deletes">
              <HelpList
                items={[
                  <>The current and previous release.</>,
                  <>Certificate-authority material.</>,
                  <>The newest full database backup.</>,
                  <>Operator-pinned artifacts.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Retention policy">
              {s ? (
                <p>
                  Keep the newest {s.keep_binaries} binaries, {s.keep_releases} releases (plus current and previous),{" "}
                  {s.keep_db} database dumps (the newest is never deleted) and {s.keep_config} config backups.
                </p>
              ) : (
                <p>The retention figures appear here once the backup status has loaded.</p>
              )}
            </HelpSection>
            <HelpSection title="Reading the figures">
              <HelpList
                items={[
                  <><strong>Rollback path</strong> is valid when both the current and the previous release are present.</>,
                  <><strong>Protected</strong> items are never deleted; <strong>Delete candidates</strong> are removed on the next cleanup run.</>,
                ]}
              />
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} />
      {db && !db.healthy && (
        <Callout tone="danger" title="Database backup problem">{db.problem}</Callout>
      )}
      {data && !data.available && (
        <Callout tone="warning" title="Backup status unavailable">{data.message}</Callout>
      )}

      {!data && !err && (
        <div aria-busy="true">
          <span className="sr-only">Loading</span>
          <StatGrid cols={4}>
            {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-[5.25rem]" />)}
          </StatGrid>
        </div>
      )}

      {db && (
        <section aria-label="Database backup">
          <StatGrid cols={2}>
            <StatCard
              label="Newest database backup"
              icon={db.healthy ? <ShieldCheck /> : <AlertTriangle />}
              tone={db.healthy ? "ok" : "err"}
              value={db.newest_at ? <span title={formatDateTime(db.newest_at)}>{ago(db.newest_at)}</span> : "None"}
              hint={db.newest_file ? db.newest_file.split("/").pop() : "No dump on this host"}
            />
            <StatCard
              label="Last backup run"
              icon={<History />}
              tone={lastFailed ? "err" : "ok"}
              value={db.last_attempt_at ? <span title={formatDateTime(db.last_attempt_at)}>{ago(db.last_attempt_at)}</span> : "Not recorded"}
              hint={lastFailed ? `Failed: ${db.last_error || "see the host log"}` : db.last_success_at ? "Succeeded" : "No run recorded yet"}
            />
          </StatGrid>
        </section>
      )}

      {s && (
        <>
          <section aria-label="Backup health figures">
            <StatGrid cols={4}>
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
                value={s.rollback_path_valid ? "Valid" : "Invalid"}
                hint="Current and previous release"
              />
              <StatCard
                label="Last cleanup"
                icon={<History />}
                value={<span title={formatDateTime(s.last_run)}>{ago(s.last_run)}</span>}
                hint={`${s.mode} · ${s.deleted_last_run} deleted · ${Math.round((s.reclaimed_kb || 0) / 1024)} MB freed`}
              />
              <StatCard
                label="Failures"
                icon={<AlertTriangle />}
                tone={s.failures ? "err" : "ok"}
                value={s.failures}
                hint={s.failure_detail || "None"}
              />
            </StatGrid>
          </section>

          <SectionCard title="Artifacts" description={current.hint} flush>
            <FilterBar
              inset
              filters={
                <FilterChips<Group>
                  label="Artifacts"
                  value={group}
                  onChange={setGroup}
                  options={GROUPS.map((g) => ({
                    value: g.value,
                    label: g.label,
                    count: itemsOf(s, g.value).length,
                    tone: g.tone === "default" ? undefined : g.tone,
                  }))}
                />
              }
            />
            {rows.length === 0 ? (
              <EmptyState size="sm" icon={<Archive />} title={`No ${current.label.toLowerCase()} artifacts`} />
            ) : (
              <Table aria-label={`${current.label} artifacts`} stickyHeader maxHeight="32rem" density="compact">
                <THead>
                  <TR><TH>Type</TH><TH>Artifact</TH><TH className="hidden md:table-cell">Reason</TH></TR>
                </THead>
                <TBody>
                  {rows.map((r, i) => (
                    <TR key={i}>
                      <TD nowrap><Badge tone={current.tone}>{r[0]}</Badge></TD>
                      <TD className="break-all font-mono text-xs">{r[1]}</TD>
                      <TD muted className="hidden text-xs md:table-cell">{r[2]}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            )}
          </SectionCard>
        </>
      )}
      {s === undefined && data?.available && (
        <EmptyState icon={<Archive />} title="No status reported" />
      )}
    </PageShell>
  );
}
