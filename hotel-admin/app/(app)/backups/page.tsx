"use client";

// Restore is hard to do by accident: only for verified backups, and it needs the backup's own name and a password.
// Download is limited to site admin and IT manager, asks for the password each time, and is not offered to other roles.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, BACKGROUND_HEADER, ListResp, BackupHealth, BackupSettings } from "@/lib/api";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { Card, CardBody, SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { RowActions } from "@/components/ui/row-actions";
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { ConsequenceList, ReadOnlyNotice, SettingField } from "@/components/ui/patterns";
import { HelpList, HelpSection } from "@/components/help";
import { useToast } from "@/components/ui/toast";
import { canWrite } from "@/lib/roles";
import { cn, errMsg, formatDate } from "@/lib/utils";
import { formatBytes } from "@/lib/bytes";
import { AccountingRetentionCard } from "./accounting-retention-card";
import { DataRetentionCard } from "./data-retention-card";
import { BackupKeyInput, BackupKeysCard } from "./backup-keys-card";
import { LedgerCard } from "./ledger-card";
import { ScheduledBackupsCard } from "./scheduled-backups-card";
import { SCHEDULE_SETTING_KEYS, backupKindLabel } from "@/lib/ledger-words";
import { submitDownload } from "@/lib/backup-download";
import {
  Archive, ShieldCheck, Download, RotateCcw, AlertTriangle, CheckCircle2, Loader2,
} from "lucide-react";
import { useOperatorRoles } from "@/lib/whoami-context";

type Artifact = {
  name: string; kind?: string; size_bytes: number; modified_at?: string;
  verified_at?: string | null; verified_tables?: number | null;
  backup_kind?: string; reason?: string; by?: string;
};
type Maintenance = { active: boolean; reason?: string; since?: string; backup?: string; unknown?: boolean };
type RestoreStep = { step: string; ok: boolean; detail?: string };
type RestoreResult = {
  present?: boolean; ok?: boolean; running?: boolean; backup?: string; summary?: string;
  started?: string; finished?: string; duration?: string; steps?: RestoreStep[];
};
type VerifyOutcome = { name: string; ok: boolean; text: string };

export default function BackupsPage() {
  const toast = useToast();
  const roles = useOperatorRoles();
  const [artifacts, setArtifacts] = useState<Artifact[] | null>(null);
  const [maint, setMaint] = useState<Maintenance | null>(null);
  const [lastRestore, setLastRestore] = useState<RestoreResult | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [verifyOutcome, setVerifyOutcome] = useState<VerifyOutcome | null>(null);
  const [verifying, setVerifying] = useState<string | null>(null);
  const [restoring, setRestoring] = useState<Artifact | null>(null);
  const [inspecting, setInspecting] = useState<Artifact | null>(null);
  const [tab, setTab] = useState("backups");

  const writable = roles ? canWrite("backups", roles) : false;
  const canDownload = roles ? canWrite("backup-download", roles) : false;
  const [downloading, setDownloading] = useState<Artifact | null>(null);
  const [downloadBusy, setDownloadBusy] = useState(false);
  const [downloadErr, setDownloadErr] = useState<string | null>(null);

  // The password buys a one-minute, one-use ticket; the file is fetched with it as a form post so the ticket never
  // appears in a URL.
  async function download({ password }: { password: string }) {
    if (!downloading) return;
    setDownloadBusy(true); setDownloadErr(null);
    try {
      const name = downloading.name;
      const t = await api.post<{ ticket: string }>(`/backup-download/${encodeURIComponent(name)}/ticket`, { password });
      submitDownload(name, t.ticket);
      setDownloading(null);
      toast.success("Download started", "The download is recorded in Activity.");
    } catch (e) { setDownloadErr(errMsg(e)); } finally { setDownloadBusy(false); }
  }

  const load = useCallback(async () => {
    try {
      const [a, m, lr] = await Promise.all([
        api.get<ListResp<Artifact>>("/backups/artifacts"),
        api.get<Maintenance>("/backups/maintenance").catch(() => ({ active: false })),
        api.get<RestoreResult>("/backups/last-restore").catch(() => ({ present: false })),
      ]);
      setArtifacts((a.data ?? []).filter((x) => x.name.startsWith("db-")));
      setMaint(m);
      setLastRestore(lr?.present ? lr : null);
    } catch (e) { setErr(errMsg(e)); }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const newest = artifacts?.[0] ?? null;
  const verifiedNewest = !!newest?.verified_at;

  const [backupOpen, setBackupOpen] = useState(false);
  const [backupBusy, setBackupBusy] = useState(false);
  const [backupErr, setBackupErr] = useState<string | null>(null);

  async function backupNow({ password }: { password: string }) {
    setBackupBusy(true); setBackupErr(null); setErr(null);
    try {
      await api.post("/backups/run", { password });
      setBackupOpen(false);
      toast.success("Backup created", "Verify it to prove it can be loaded back.");
      await load();
    } catch (e) { setBackupErr(errMsg(e)); } finally { setBackupBusy(false); }
  }

  const [keyFor, setKeyFor] = useState<string | null>(null);
  const [verifyKey, setVerifyKey] = useState("");
  const startVerify = (name: string) => {
    if (isEncrypted(name)) { setVerifyKey(""); setKeyFor(name); } else void verify(name);
  };

  async function verify(name: string, identity?: string) {
    setVerifying(name); setErr(null); setVerifyOutcome(null);
    try {
      const r = await api.post<{ ok: boolean; tables: number; duration: string; detail?: string }>(
        `/backups/artifacts/${encodeURIComponent(name)}/verify`, identity ? { identity } : undefined);
      if (r.ok) {
        toast.success("Backup verified", `It loads and contains ${r.tables} tables (${r.duration}).`);
        setVerifyOutcome({ name, ok: true, text: `Verified: the backup loads and contains ${r.tables} tables (${r.duration}).` });
      } else {
        setVerifyOutcome({ name, ok: false, text: `This backup did NOT verify: ${r.detail ?? "it could not be loaded"}.` });
      }
      await load();
    } catch (e) { setErr(errMsg(e)); } finally { setVerifying(null); }
  }

  const openBackup = () => { setBackupErr(null); setBackupOpen(true); };

  const verifyLabel = (a: Artifact) => (verifying === a.name ? "Checking…" : a.verified_at ? "Re-verify" : "Verify");
  const startDownload = (a: Artifact) => { setDownloadErr(null); setDownloading(a); };

  return (
    <PageShell>
      <PageHeader
        title="Backups"
        description="A complete copy of this site’s data: on a schedule you set, and on demand."
        help={
          <>
            <HelpSection title="Back up, verify, download, restore">
              <HelpList
                items={[
                  <><strong>Back up now</strong> writes a complete copy of this site&rsquo;s data to the appliance. It takes a few minutes and clients are not affected. Scheduled backups, if you switch them on, are taken the same way.</>,
                  <><strong>Verify</strong> proves a backup can actually be loaded back. Only a verified backup can be restored.</>,
                  <><strong>Download</strong> keeps a copy off the appliance. Only a site admin or the IT manager can download, and it asks for your password every time. Every download is recorded.</>,
                  <><strong>Restore</strong> replaces the current data with the backup. It asks for the backup&rsquo;s name typed out and your password.</>,
                  <><strong>Encryption</strong>: once a key holder is set up, every backup is encrypted to the site&rsquo;s own keys. Verifying or restoring one asks for a key holder&rsquo;s key; downloads stay encrypted.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="How a restore protects you">
              <p>
                The appliance takes a fresh safety copy first, loads the backup into a separate database, swaps the two
                while keeping the current one, checks the result and puts the original back if the check fails. The
                outcome is recorded on the appliance, so it is shown here even if this page disconnects while the
                services restart.
              </p>
            </HelpSection>
            <HelpSection title="Storage & retention">
              <p>
                The nightly sweep keeps the disk from filling up (it never deletes a backup that the backup settings keep); the retention
                values decide how far back you can recover. Each value has a unit, a default and an allowed range, and changing them is recorded against
                your account.
              </p>
              <p>
                The sweep never removes the current or previous release, the newest database backup, identity,
                certificates and trust material, or anything an operator pinned.
              </p>
            </HelpSection>
          </>
        }
        actions={writable && <Button onClick={openBackup}><Archive /> Back up now</Button>}
      />

      {roles !== null && !writable && (
        <ReadOnlyNotice>
          {canDownload
            ? "Your role can see and download backups, but not take, verify or restore them."
            : "Your role can see the backups, but not download, take, verify or restore them. A backup is a complete copy of this site’s data."}
        </ReadOnlyNotice>
      )}

      {maint?.active && (
        <Callout tone="warning" icon={<Loader2 className="animate-spin motion-reduce:animate-none" />}
          title="This appliance is not serving clients right now">
          {maint.reason}{maint.since ? ` · started ${formatDate(maint.since)}` : ""}
        </Callout>
      )}

      <ErrorBanner err={err} className="mb-0" />
      {verifyOutcome && !verifyOutcome.ok && (
        <Callout tone="danger" title="Verification failed">{verifyOutcome.text}</Callout>
      )}

      <Card>
        <CardBody>
          {artifacts === null ? (
            err ? (
              <p className="text-sm text-muted-foreground">Recovery readiness appears once the backups can be read.</p>
            ) : (
              <div className="flex items-start gap-3" aria-busy="true">
                <Skeleton className="size-9 rounded-full" />
                <div className="flex-1 space-y-2"><Skeleton className="h-4 w-2/3" /><Skeleton className="h-3 w-1/2" /></div>
              </div>
            )
          ) : (
            <div className="flex flex-wrap items-center justify-between gap-4">
              <div className="flex min-w-0 items-start gap-3">
                <span
                  className={cn(
                    "inline-flex size-9 shrink-0 items-center justify-center rounded-full",
                    newest && verifiedNewest ? "bg-success-subtle text-success" : "bg-warning-subtle text-warning-subtle-foreground",
                  )}
                  aria-hidden
                >
                  {newest && verifiedNewest ? <CheckCircle2 className="size-5" /> : <AlertTriangle className="size-5" />}
                </span>
                <div className="min-w-0">
                  <p className="text-emphasis">
                    {!newest
                      ? "There is no backup of this site yet"
                      : verifiedNewest
                        ? "This site can be recovered from its latest backup"
                        : "The latest backup has not been checked yet"}
                  </p>
                  <p className="text-sm text-muted-foreground">
                    {!newest
                      ? "Nothing could be recovered if the appliance failed today."
                      : <>
                          Taken {newest.modified_at ? formatDate(newest.modified_at) : "recently"} · {formatBytes(newest.size_bytes)}
                          {verifiedNewest
                            ? ` · verified ${formatDate(newest.verified_at!)}`
                            : " · verifying proves it can actually be loaded back"}
                        </>}
                  </p>
                </div>
              </div>
              {writable && newest && !verifiedNewest && (
                <Button variant="secondary" onClick={() => startVerify(newest.name)} disabled={verifying === newest.name}>
                  <ShieldCheck /> {verifying === newest.name ? "Checking…" : "Verify it"}
                </Button>
              )}
              {writable && !newest && (
                <Button variant="secondary" onClick={openBackup}>Back up now</Button>
              )}
            </div>
          )}
        </CardBody>
      </Card>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList aria-label="Backup sections">
          <TabsTrigger value="backups">Backups</TabsTrigger>
          <TabsTrigger value="schedule">Schedule &amp; storage</TabsTrigger>
          <TabsTrigger value="ledger">Erasure ledger</TabsTrigger>
          <TabsTrigger value="retention">Data retention</TabsTrigger>
        </TabsList>

        <TabsContent value="backups" className="mt-5 space-y-5">
          <SectionCard
            flush
            title="Available backups"
            description="Only a verified backup can be restored. Select one for its details."
          >
            {artifacts === null ? (
              err ? null : <SkeletonRows rows={3} cols={4} />
            ) : artifacts.length === 0 ? (
              <EmptyState size="sm" icon={<Archive />} title="No backups yet"
                hint="Use Back up now, or switch on scheduled backups."
                action={writable ? <Button variant="secondary" size="sm" onClick={openBackup}>Back up now</Button> : undefined} />
            ) : (
              <Table label="Available backups">
                <THead>
                  <TR>
                    <TH>Backup</TH>
                    <TH>Status</TH>
                    <TH numeric className="hidden sm:table-cell">Size</TH>
                    <TH className="hidden md:table-cell">Checked</TH>
                    <TH><span className="sr-only">Actions</span></TH>
                  </TR>
                </THead>
                <TBody>
                  {artifacts.map((a) => (
                    <TR key={a.name} onClick={() => setInspecting(a)} selected={inspecting?.name === a.name}>
                      <TD className="min-w-48">
                        <div className="font-medium">{a.modified_at ? formatDate(a.modified_at) : a.name}</div>
                        <div className="max-w-[22rem] truncate font-mono text-2xs text-muted-foreground" title={a.name}>{a.name}</div>
                      </TD>
                      <TD>
                        <div className="flex flex-wrap gap-1">
                          {a.backup_kind && <Badge tone="default">{backupKindLabel(a.backup_kind)}</Badge>}
                          {a.verified_at
                            ? <StatusBadge tone="ok">Verified</StatusBadge>
                            : <StatusBadge>Not checked</StatusBadge>}
                          {isEncrypted(a.name) && <Badge tone="info">Encrypted</Badge>}
                        </div>
                      </TD>
                      <TD numeric className="hidden sm:table-cell">{formatBytes(a.size_bytes)}</TD>
                      <TD nowrap muted className="hidden md:table-cell">
                        {a.verified_at
                          ? `${formatDate(a.verified_at)}${a.verified_tables ? ` · ${a.verified_tables} tables` : ""}`
                          : "—"}
                      </TD>
                      <TD className="w-0">
                        <RowActions
                          subject={a.name}
                          primary={{
                            label: verifyLabel(a),
                            icon: <ShieldCheck />,
                            hidden: !writable,
                            disabled: verifying === a.name,
                            onSelect: () => startVerify(a.name),
                          }}
                          items={[
                            { label: "Download…", icon: <Download />, hidden: !canDownload, onSelect: () => startDownload(a) },
                            {
                              label: "Restore…",
                              icon: <RotateCcw />,
                              danger: true,
                              hidden: !(writable && a.verified_at),
                              onSelect: () => setRestoring(a),
                            },
                          ]}
                        />
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            )}
          </SectionCard>
          {lastRestore && <LastRestoreCard result={lastRestore} />}
          <BackupKeysCard writable={writable} />
        </TabsContent>

        <TabsContent value="schedule" className="mt-5 space-y-5">
          <ScheduledBackupsCard writable={writable} />
          <StorageAndRetention writable={writable} />
        </TabsContent>

        <TabsContent value="ledger" className="mt-5 space-y-5">
          <LedgerCard writable={writable} />
        </TabsContent>

        <TabsContent value="retention" className="mt-5 space-y-5">
          <AccountingRetentionCard writable={writable} />
          <DataRetentionCard writable={writable} />
        </TabsContent>
      </Tabs>

      <DetailsDrawer
        open={inspecting !== null}
        onOpenChange={(v) => !v && setInspecting(null)}
        eyebrow="Backup"
        title={inspecting ? (inspecting.modified_at ? formatDate(inspecting.modified_at) : inspecting.name) : ""}
        badges={inspecting && (
          <>
            {inspecting.verified_at ? <StatusBadge tone="ok">Verified</StatusBadge> : <StatusBadge>Not checked</StatusBadge>}
            {isEncrypted(inspecting.name) && <Badge tone="info">Encrypted</Badge>}
          </>
        )}
        actions={inspecting && (writable || canDownload) ? (
          <>
            {canDownload && (
              <Button variant="secondary" onClick={() => { const a = inspecting; setInspecting(null); startDownload(a); }}>
                <Download /> Download…
              </Button>
            )}
            {writable && (
              <Button variant="secondary" disabled={verifying === inspecting.name}
                onClick={() => { const n = inspecting.name; setInspecting(null); startVerify(n); }}>
                <ShieldCheck /> {verifyLabel(inspecting)}
              </Button>
            )}
            {writable && inspecting.verified_at && (
              <Button variant="danger" onClick={() => { const a = inspecting; setInspecting(null); setRestoring(a); }}>
                <RotateCcw /> Restore…
              </Button>
            )}
          </>
        ) : undefined}
      >
        {inspecting && (
          <DetailsSection title="Record">
            <DescriptionList
              items={[
                { label: "File", value: <span className="break-all font-mono text-xs">{inspecting.name}</span> },
                { label: "Kind", value: inspecting.backup_kind ? backupKindLabel(inspecting.backup_kind) : "—" },
                { label: "Taken", value: inspecting.modified_at ? formatDate(inspecting.modified_at) : "—" },
                { label: "Size", value: <span className="tabular">{formatBytes(inspecting.size_bytes)}</span> },
                { label: "Reason", value: inspecting.reason, hidden: !inspecting.reason },
                { label: "Taken by", value: inspecting.by, hidden: !inspecting.by },
                { label: "Verified", value: inspecting.verified_at ? formatDate(inspecting.verified_at) : "Not checked" },
                { label: "Tables loaded", value: inspecting.verified_tables, hidden: !inspecting.verified_tables },
                { label: "Encryption", value: isEncrypted(inspecting.name) ? "Encrypted to the hotel’s keys" : "Not encrypted" },
              ]}
            />
          </DetailsSection>
        )}
      </DetailsDrawer>

      <ConfirmDialog
        open={backupOpen}
        onOpenChange={(v) => { if (!v) { setBackupOpen(false); setBackupErr(null); } }}
        title="Back up this site now?"
        description="A backup writes a complete copy of this site’s data to the appliance. It takes a moment and clients are not affected."
        confirmLabel="Back up now"
        busy={backupBusy}
        error={backupErr}
        requirePassword
        onConfirm={backupNow}
      />

      <ConfirmDialog
        open={downloading !== null}
        onOpenChange={(v) => { if (!v) { setDownloading(null); setDownloadErr(null); } }}
        title="Download this backup?"
        description="A backup is a complete copy of this site’s data: client accounts, stays, sessions and the activity trail. Keep the file somewhere only authorised people can reach. The download is recorded against your account."
        confirmLabel="Download"
        busy={downloadBusy}
        error={downloadErr}
        requirePassword
        onConfirm={download}
      />

      <DialogForm open={keyFor !== null} onOpenChange={(v) => { if (!v) { setKeyFor(null); setVerifyKey(""); } }}
        title="Verify an encrypted backup"
        description="This backup is encrypted to the hotel’s keys. A key holder’s key opens it for this check only; the appliance does not keep it."
        submitLabel="Verify" disabled={verifyKey.trim() === ""}
        onSubmit={() => { const n = keyFor!, k = verifyKey; setKeyFor(null); setVerifyKey(""); void verify(n, k); }}>
        <BackupKeyInput value={verifyKey} onChange={setVerifyKey} />
      </DialogForm>

      {restoring && (
        <RestoreDialog
          backup={restoring}
          onClose={() => setRestoring(null)}
          onDone={async (r) => {
            setRestoring(null); setLastRestore(r);
            if (r.ok) toast.success("Restore finished", r.summary);
            else toast.error("Restore did not complete", r.summary);
            await load();
          }}
        />
      )}
    </PageShell>
  );
}

function LastRestoreCard({ result }: { result: RestoreResult }) {
  // A restore in progress (ok:false until it finishes) must not render as a failure.
  const running = result.running === true;
  return (
    <SectionCard
      title={
        <span className="flex items-center gap-2">
          {running ? <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden />
            : result.ok ? <CheckCircle2 className="size-4 text-success" aria-hidden />
                        : <AlertTriangle className="size-4 text-destructive" aria-hidden />}
          {running ? "Restore in progress" : "Last restore"}
        </span>
      }
      actions={!running && (
        <StatusBadge tone={result.ok ? "ok" : "err"}>{result.ok ? "Succeeded" : "Did not complete"}</StatusBadge>
      )}
      bodyClassName="space-y-3 text-sm"
    >
      <p>
        {running
          ? "The appliance is replacing the database. It stops serving clients until this finishes. Do not start another restore."
          : result.summary}
      </p>
      <p className="text-caption text-muted-foreground">
        <span className="font-mono">{result.backup}</span>
        {running
          ? (result.started ? ` · started ${formatDate(result.started)}` : "")
          : <>{result.finished ? ` · ${formatDate(result.finished)}` : ""}{result.duration ? ` · took ${result.duration}` : ""}</>}
      </p>
      {result.steps && result.steps.length > 0 && (
        <ol className="flex flex-wrap gap-x-4 gap-y-1.5">
          {result.steps.map((s, i) => (
            <li key={i} className="flex items-start gap-1.5 text-xs">
              {s.ok ? <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-success" aria-hidden />
                    : <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-destructive" aria-hidden />}
              <span>
                <span className="sr-only">{s.ok ? "Done: " : "Not done: "}</span>
                <strong className="font-semibold">{s.step.replace(/_/g, " ")}</strong>{s.detail ? ` — ${s.detail}` : ""}
              </span>
            </li>
          ))}
        </ol>
      )}
    </SectionCard>
  );
}

function isEncrypted(name: string) { return name.endsWith(".age"); }

function RestoreDialog({ backup, onClose, onDone }: {
  backup: Artifact;
  onClose: () => void;
  onDone: (r: RestoreResult) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [started, setStarted] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [identity, setIdentity] = useState("");
  const encrypted = isEncrypted(backup.name);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);

  // Return as soon as the appliance accepts: the restore restarts edged, so waiting on this request would show a
  // timeout as failure and invite a second destructive operation.
  async function go({ password, confirm }: { password: string; confirm: string }) {
    if (encrypted && identity.trim() === "") { setErr("This backup is encrypted: paste a key holder’s backup key."); return; }
    setBusy(true); setErr(null);
    try {
      await api.post(`/backups/artifacts/${encodeURIComponent(backup.name)}/restore`,
        encrypted ? { password, confirm, identity } : { password, confirm });
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : errMsg(e));
      setBusy(false);
      return;
    }
    setStarted(true);
    while (alive.current) {
      await new Promise((r) => setTimeout(r, 4000));
      let r: RestoreResult | null = null;
      // A failed poll means edged is restarting; keep asking.
      try { r = await api.get<RestoreResult>("/backups/last-restore", { [BACKGROUND_HEADER]: "1" }); } catch { continue; }
      if (r?.present && r.running === false) { onDone(r); return; }
    }
  }

  const taken = backup.modified_at ? formatDate(backup.modified_at) : "it was taken";

  return (
    <ConfirmDialog
      open
      onOpenChange={(v) => { if (!v && !busy) onClose(); }}
      title="Restore this backup?"
      confirmLabel={started ? "Restoring" : "Restore this backup"}
      confirmVariant="danger"
      busy={busy}
      error={err}
      confirmText={backup.name}
      requirePassword
      onConfirm={({ password }) => go({ password, confirm: backup.name })}
    >
      <ConsequenceList
        title="This site’s current data will be replaced"
        items={[
          <>Everything recorded since {taken} — stays, sessions, client accounts, settings and the activity trail — is replaced by what the backup contains.</>,
          <>The appliance stops serving clients and staff while it works, usually for several minutes.</>,
        ]}
      />

      <div className="rounded-md border border-border bg-surface p-3">
        <div className="text-caption text-muted-foreground">Restoring</div>
        <div className="font-medium">{backup.modified_at ? formatDate(backup.modified_at) : backup.name}</div>
        <div className="break-all font-mono text-2xs text-muted-foreground">{backup.name}</div>
        <div className="mt-1 text-caption text-muted-foreground">
          {formatBytes(backup.size_bytes)}
          {backup.verified_at ? ` · verified ${formatDate(backup.verified_at)}` : ""}
        </div>
      </div>

      {encrypted && !started && <BackupKeyInput value={identity} onChange={setIdentity} />}

      <div>
        <div className="mb-1.5 text-label">What the appliance will do</div>
        <ol className="list-decimal space-y-1 ps-5 text-sm text-muted-foreground">
          <li>Take a fresh safety copy of the current data first.</li>
          <li>Stop serving clients and staff while it works.</li>
          <li>Load the backup into a separate database — if it will not load, nothing is replaced.</li>
          <li>Swap the two, keeping the current database rather than deleting it.</li>
          <li>Check the restored database, and put the original back if the check fails.</li>
          <li>Start serving again.</li>
        </ol>
      </div>

      {started && (
        <div role="status" aria-live="polite" className="flex items-start gap-2.5 rounded-md border border-info/25 bg-info-subtle px-3.5 py-3 text-sm text-info-subtle-foreground">
          <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden />
          <p>
            <strong className="font-semibold">Restoring — this window stays open until it finishes.</strong>{" "}
            The appliance has accepted the restore and is working through it. This can take several minutes. The
            outcome is recorded on the appliance, so if this page disconnects — it will, briefly, because the
            services are restarted — you will still see what happened when it comes back. Do not start another
            restore.
          </p>
        </div>
      )}
    </ConfirmDialog>
  );
}

function StorageAndRetention({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [health, setHealth] = useState<BackupHealth | null>(null);
  const [settings, setSettings] = useState<BackupSettings | null>(null);
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [schedule, setSchedule] = useState("");
  const [settingsPw, setSettingsPw] = useState("");
  const [saving, setSaving] = useState(false);
  const [saveErr, setSaveErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [h, s0] = await Promise.all([
        api.get<BackupHealth>("/backups/health"),
        api.get<BackupSettings>("/backups/settings"),
      ]);
      const s = { ...s0, retention: s0.retention.filter((x) => !(SCHEDULE_SETTING_KEYS as readonly string[]).includes(x.key)) };
      setHealth(h);
      setSettings(s);
      setEdits(Object.fromEntries(s.retention.map((x) => [x.key, String(x.value)])));
      setSchedule(s.schedule ?? "");
    } catch { /* the section reports its own absence below rather than breaking the page */ }
  }, []);
  useEffect(() => { load(); }, [load]);

  const valueOf = (key: string, fallback: number) => {
    const raw = edits[key];
    return raw === undefined || raw.trim() === "" ? fallback : Number(raw);
  };
  const dirty = !!settings && (
    settings.retention.some((x) => valueOf(x.key, x.value) !== x.value) ||
    (schedule !== (settings.schedule ?? ""))
  );
  const outOfRange = !!settings && settings.retention.some((x) => {
    const v = valueOf(x.key, x.value);
    return !Number.isFinite(v) || v < x.min || v > x.max;
  });

  async function save() {
    if (!settings) return;
    setSaving(true); setSaveErr(null);
    try {
      await api.put("/backups/settings", {
        retention: Object.fromEntries(settings.retention.map((x) => [x.key, valueOf(x.key, x.value)])),
        schedule,
        password: settingsPw,
      });
      toast.success("Retention policy saved");
      setSettingsPw("");
      await load();
    } catch (e) { setSaveErr(errMsg(e)); } finally { setSaving(false); }
  }

  const ret = (health?.retention ?? {}) as Record<string, unknown>;
  const diskPct = Number(ret.disk_pct ?? 0);

  return (
    <SectionCard
      title="Storage & retention"
      description="Disk used, the nightly sweep, and how long things are kept."
      bodyClassName="space-y-5"
    >
      <StatGrid cols={3}>
        <StatCard
          label="Disk used"
          value={health?.retention_readable ? `${diskPct}%` : "—"}
          tone={health?.retention_readable
            ? (diskPct >= Number(ret.disk_crit ?? 101) ? "err" : diskPct >= Number(ret.disk_warn ?? 101) ? "warn" : "default")
            : "default"}
          hint={health?.retention_readable
            ? `Warns at ${ret.disk_warn ?? "—"}%, critical at ${ret.disk_crit ?? "—"}%`
            : (health?.retention_error ?? "Checking…")}
        />
        <StatCard
          label="Backups kept"
          value={health ? String(health.database_backups ?? 0) : "—"}
          hint={health?.newest_database_backup ? `Newest ${formatDate(health.newest_database_backup)}` : undefined}
        />
        <StatCard
          label="Nightly sweep"
          value={<span className="text-subtitle">{health ? (health.timer_readable ? (health.timer_active ? "Scheduled" : "Not scheduled") : "Unknown") : "—"}</span>}
          hint={settings?.schedule_known ? `Runs at ${settings.schedule}` : undefined}
        />
      </StatGrid>

      {health?.retention_readable && Number(ret.failures ?? 0) > 0 && (
        <Callout tone="danger" title={`${String(ret.failures)} failure(s) on the last sweep`}>
          {String(ret.failure_detail || "no detail")}
        </Callout>
      )}

      {settings && (
        <>
          <p className="text-sm text-muted-foreground">
            {settings.config_present
              ? "What the nightly sweep keeps, and when it runs. These values are stored on the appliance."
              : "No policy has been saved yet, so the built-in defaults below are in force."}
          </p>
          <div className="grid gap-5 sm:grid-cols-2">
            {settings.retention.map((sIt) => (
              <SettingField
                key={sIt.key}
                label={sIt.label}
                unit={sIt.unit}
                min={sIt.min}
                max={sIt.max}
                defaultValue={sIt.default}
                explanation={sIt.explains}
                readOnly={!writable}
                value={edits[sIt.key] ?? String(sIt.value)}
                onChange={(v) => setEdits((m) => ({ ...m, [sIt.key]: v }))}
              />
            ))}
            <Field
              label="Nightly sweep runs at"
              hint={<>Appliance local time.{" "}
                {settings.schedule_known
                  ? `Currently scheduled for ${settings.schedule}.`
                  : "The current schedule could not be read from the appliance."}</>}
            >
              <Input type="time" value={schedule} disabled={!writable} className="w-40"
                onChange={(e) => setSchedule(e.target.value)} />
            </Field>
          </div>

          {writable && dirty && (
            <div className="space-y-3 rounded-md border border-border bg-surface p-4">
              <ErrorBanner err={saveErr} className="mb-0" />
              <Field label="Confirm your password" className="max-w-sm"
                hint="Changing what is kept, and when, is recorded against your account.">
                <Input type="password" autoComplete="current-password" value={settingsPw}
                  onChange={(e) => setSettingsPw(e.target.value)} />
              </Field>
              <div className="flex flex-wrap gap-2">
                <Button disabled={saving || !settingsPw || outOfRange} onClick={save}>
                  {saving ? "Saving…" : "Save retention policy"}
                </Button>
                <Button variant="ghost" disabled={saving} onClick={() => {
                  setEdits(Object.fromEntries(settings.retention.map((x) => [x.key, String(x.value)])));
                  setSchedule(settings.schedule ?? "");
                  setSettingsPw("");
                  setSaveErr(null);
                }}>Discard changes</Button>
              </div>
            </div>
          )}

          <p className="text-caption text-muted-foreground">
            {String(ret.retained ?? 0)} artefacts retained, {String(ret.protected ?? 0)} protected and never
            deleted, {String(ret.pinned ?? 0)} pinned by an operator.
          </p>
        </>
      )}
    </SectionCard>
  );
}
