"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type BackupSettings } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Switch } from "@/components/ui/misc";
import { SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";
import { SCHEDULE_SETTING_KEYS, backupHeadline, backupKindLabel, type BackupStatus } from "@/lib/ledger-words";

export const EARLIER_CONFIRMATION = "remove earlier backups";

const mb = (n: number) => `${(n / 1048576).toFixed(1)} MB`;

export function ScheduledBackupsCard({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [d, setD] = useState<BackupStatus | null>(null);
  const [settings, setSettings] = useState<BackupSettings | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [saveOpen, setSaveOpen] = useState(false);
  const [saveErr, setSaveErr] = useState<string | null>(null);
  const [runOpen, setRunOpen] = useState(false);
  const [runErr, setRunErr] = useState<string | null>(null);
  const [earlierOpen, setEarlierOpen] = useState(false);
  const [earlierErr, setEarlierErr] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const load = useCallback(async () => {
    try {
      const [r, s] = await Promise.all([
        api.get<BackupStatus>("/backups/status"),
        api.get<BackupSettings>("/backups/settings").catch(() => null),
      ]);
      setD(r && r.state ? { ...r, backups: r.backups ?? [], problems: r.problems ?? [], earlier: r.earlier ?? { count: 0, size_bytes: 0, unencrypted: 0, oldest: null, sets: [] } } : null);
      if (s) {
        setSettings(s);
        setEdits(Object.fromEntries(s.retention.filter((x) => (SCHEDULE_SETTING_KEYS as readonly string[]).includes(x.key)).map((x) => [x.key, String(x.value)])));
      }
      setErr(null);
    } catch (e) { setErr(errMsg(e)); }
  }, []);
  useEffect(() => { void load(); return () => { if (timer.current) clearTimeout(timer.current); }; }, [load]);
  useEffect(() => {
    if (d?.running) timer.current = setTimeout(() => void load(), 5000);
    return () => { if (timer.current) clearTimeout(timer.current); };
  }, [d, load]);

  const fields = (settings?.retention ?? []).filter((x) => (SCHEDULE_SETTING_KEYS as readonly string[]).includes(x.key));
  const byKey = Object.fromEntries(fields.map((x) => [x.key, x]));
  const valueOf = (key: string) => {
    const raw = edits[key];
    return raw === undefined || raw.trim() === "" ? (byKey[key]?.value ?? 0) : Number(raw);
  };
  const dirty = fields.some((x) => valueOf(x.key) !== x.value);
  // The two pools together are capped (five sets, PO policy); the cap comes from the appliance, so an older one is not judged by it.
  const maxTotal = d?.retention?.max_total;
  const overTotal = Boolean(maxTotal && byKey.BACKUP_KEEP && byKey.SAFETY_BACKUP_KEEP
    && valueOf("BACKUP_KEEP") + valueOf("SAFETY_BACKUP_KEEP") > maxTotal);
  const outOfRange = overTotal || fields.some((x) => { const v = valueOf(x.key); return !Number.isFinite(v) || v < x.min || v > x.max; });
  const enabled = valueOf("BACKUP_ENABLED") === 1;
  const replicate = valueOf("BACKUP_REPLICATE") === 1;
  const [copying, setCopying] = useState(false);
  const copyState = new Map((d?.mirror?.sets ?? []).map((s) => [s.name, s]));

  async function copyNow() {
    setCopying(true); setErr(null);
    try {
      // scd starts the copy and answers at once; the table shows each set as it is read back.
      await api.post("/backups/mirror/run", {});
      await load();
    } catch (e) { setErr(e instanceof ApiError ? e.message : errMsg(e)); } finally { setCopying(false); }
  }

  async function save({ password }: { password: string }) {
    setSaveErr(null);
    try {
      await api.put("/backups/settings", {
        retention: Object.fromEntries(fields.map((x) => [x.key, valueOf(x.key)])),
        schedule: "",
        password,
      });
      setSaveOpen(false);
      toast.success("Backup settings saved", enabled ? "Scheduled backups are on." : "Scheduled backups are off.");
      await load();
    } catch (e) { setSaveErr(e instanceof ApiError ? e.message : errMsg(e)); }
  }

  async function run({ password }: { password: string }) {
    setRunErr(null);
    try {
      await api.post("/backups/run", { password });
      setRunOpen(false);
      toast.success("Backup taken", "It is encrypted and was proven restorable before it was kept.");
      await load();
    } catch (e) { setRunErr(e instanceof ApiError ? e.message : errMsg(e)); }
  }

  async function removeEarlier({ password }: { password: string }) {
    setEarlierErr(null);
    try {
      await api.post("/backups/earlier/remove", { password, confirm: EARLIER_CONFIRMATION });
      setEarlierOpen(false);
      toast.success("Earlier backups removed");
      await load();
    } catch (e) { setEarlierErr(e instanceof ApiError ? e.message : errMsg(e)); }
  }

  const head = backupHeadline(d);
  const haveVerifiedNew = !!d?.backups.some((b) => b.restorable && (b.kind === "scheduled" || b.kind === "manual"));
  const earlier = d?.earlier;

  return (
    <SectionCard
      title="Scheduled backups"
      description="Database, configuration and keys, encrypted and proven restorable. Off until you switch them on."
      actions={writable && (
        <Button size="sm" variant="secondary" disabled={!!d?.running || d?.state === "needs_key_holder"} onClick={() => { setRunErr(null); setRunOpen(true); }}>
          {d?.running ? "A backup is running…" : "Take a backup now"}
        </Button>
      )}
      bodyClassName="space-y-4"
    >
        <ErrorBanner err={err} />
        <Callout tone={head.tone === "ok" ? "success" : head.tone === "err" ? "danger" : "warning"} title={head.title}>{head.detail}</Callout>

        {d && (
          <p className="text-sm text-muted-foreground">
            {d.schedule.next ? <>Next scheduled backup: <strong>{formatDate(d.schedule.next)}</strong>. </> : null}
            {d.last_error ? <>The last attempt failed: {d.last_error}. </> : null}
            Backups before a restore, a migration or a deployment are always taken.
          </p>
        )}

        {settings && fields.length > 0 && (
          <div className="space-y-4 rounded-md border border-border bg-surface/60 p-4">
            <div className="flex items-center gap-3">
              <Switch
                id="scheduled-backups-enabled"
                label="Scheduled backups"
                checked={enabled}
                disabled={!writable}
                onCheckedChange={(v) => setEdits((m) => ({ ...m, BACKUP_ENABLED: v ? "1" : "0" }))}
              />
              <label htmlFor="scheduled-backups-enabled" className="text-emphasis">
                {enabled ? "Scheduled backups are on" : "Scheduled backups are off"}
              </label>
            </div>
            {byKey.BACKUP_REPLICATE && (
              <div className="space-y-1">
                <div className="flex items-center gap-3">
                  <Switch
                    id="backup-replicate"
                    label="Copy backups to the secondary storage"
                    checked={replicate}
                    disabled={!writable}
                    onCheckedChange={(v) => setEdits((m) => ({ ...m, BACKUP_REPLICATE: v ? "1" : "0" }))}
                  />
                  <label htmlFor="backup-replicate" className="text-emphasis">
                    {replicate ? "Local and secondary storage" : "Local only"}
                  </label>
                </div>
                <p className="text-caption text-muted-foreground">{byKey.BACKUP_REPLICATE.explains}</p>
              </div>
            )}
            <div className="grid gap-5 sm:grid-cols-2">
              {fields.filter((x) => x.key !== "BACKUP_ENABLED" && x.key !== "BACKUP_REPLICATE").map((x) => (
                <SettingField
                  key={x.key}
                  label={x.label}
                  unit={x.unit}
                  min={x.min}
                  max={x.max}
                  defaultValue={x.default}
                  explanation={x.explains}
                  readOnly={!writable}
                  value={edits[x.key] ?? String(x.value)}
                  onChange={(v) => setEdits((m) => ({ ...m, [x.key]: v }))}
                />
              ))}
            </div>
            {overTotal && (
              <p className="text-sm text-destructive">
                At most {maxTotal} backups are kept in all: backups kept and safety backups kept add up to {valueOf("BACKUP_KEEP") + valueOf("SAFETY_BACKUP_KEEP")}.
              </p>
            )}
            {writable && dirty && (
              <div className="flex flex-wrap gap-2">
                <Button disabled={outOfRange} onClick={() => { setSaveErr(null); setSaveOpen(true); }}>Save backup settings</Button>
                <Button variant="ghost" onClick={() => setEdits(Object.fromEntries(fields.map((x) => [x.key, String(x.value)])))}>Discard changes</Button>
              </div>
            )}
          </div>
        )}

        {d?.retention && (
          <p className="text-sm text-muted-foreground">
            At most {d.retention.max_total} backups are kept: {d.retention.regular} scheduled or manual and {d.retention.protective} taken
            before a change. A new one is verified before an older one is removed.
            {d.retention.held > 0 ? ` ${d.retention.held} earlier backup${d.retention.held === 1 ? " is" : "s are"} kept as well: they are never removed automatically (see below).` : ""}
          </p>
        )}

        {d?.mirror && d.mirror.mode === "local_and_secondary" && (
          <Callout
            tone={d.mirror.state === "ok" ? "success" : d.mirror.state === "pending" ? "info" : "warning"}
            title={d.mirror.state === "ok" ? "Backups copied to the secondary storage" : "Copy to the secondary storage"}
          >
            {d.mirror.message}
            {d.mirror.last_ok ? ` Last complete copy ${formatDate(d.mirror.last_ok)}.` : ""}
            {d.mirror.running ? " Copying now: refresh to see each backup as it is read back." : ""}
            {writable && d.mirror.state !== "ok" && d.mirror.state !== "no_destination" && (
              <span className="mt-2 block">
                <Button size="sm" variant="secondary" disabled={copying} onClick={() => void copyNow()}>{copying ? "Copying…" : "Copy now"}</Button>
              </span>
            )}
          </Callout>
        )}

        {d && d.backups.length > 0 && (
          <div className="overflow-hidden rounded-md border border-border">
            <Table label="Backups taken" density="compact">
              <THead><TR><TH>Taken</TH><TH>Kind</TH><TH numeric>Size</TH><TH>Restorable</TH><TH numeric className="hidden sm:table-cell">Ledger entries</TH>{d.mirror?.mode === "local_and_secondary" && <TH>Copy</TH>}</TR></THead>
              <TBody>
                {d.backups.map((b) => (
                  <TR key={b.name}>
                    <TD nowrap>{formatDate(b.taken_at)}</TD>
                    <TD>
                      <Badge tone={b.kind === "scheduled" || b.kind === "manual" ? "info" : "default"}>{backupKindLabel(b.kind)}</Badge>
                      {(b.reason || b.by) && <div className="text-2xs text-muted-foreground">{[b.reason, b.by && `by ${b.by}`].filter(Boolean).join(" · ")}</div>}
                    </TD>
                    <TD numeric>{mb(b.size_bytes)}</TD>
                    <TD nowrap><StatusBadge tone={b.restorable ? "ok" : "err"}>{b.restorable ? "Yes" : "No"}</StatusBadge></TD>
                    <TD numeric className="hidden sm:table-cell">{b.ledger_seq}</TD>
                    {d.mirror?.mode === "local_and_secondary" && (
                      <TD nowrap title={copyState.get(b.name)?.error ?? undefined}>
                        <StatusBadge tone={copyState.get(b.name)?.state === "copied" ? "ok" : copyState.get(b.name)?.state === "failed" ? "err" : "warn"}>
                          {copyState.get(b.name)?.state === "copied" ? "Copied, read back" : copyState.get(b.name)?.state === "failed" ? "Failed" : "Pending"}
                        </StatusBadge>
                      </TD>
                    )}
                  </TR>
                ))}
              </TBody>
            </Table>
          </div>
        )}

        {d?.problems && d.problems.length > 0 && (
          <Callout tone="danger" title="A stored backup changed">{d.problems.map((p) => `${p.name}: ${p.detail}`).join("; ")}</Callout>
        )}

        {earlier && earlier.count > 0 && (
          <div className="space-y-3 rounded-md border border-border bg-surface/60 p-4">
            <div>
              <p className="text-emphasis">Earlier backups ({earlier.count}, {mb(earlier.size_bytes)})</p>
              <p className="text-sm text-muted-foreground">
                Made before the one backup system and never removed automatically. A verified earlier dump can be
                restored from Available backups.
                {earlier.unencrypted > 0 ? ` ${earlier.unencrypted} of them are not encrypted.` : ""}
              </p>
            </div>
            <div className="overflow-hidden rounded-md border border-border bg-card">
              <Table label="Earlier backups" density="compact">
                <THead><TR><TH>Name</TH><TH>Kind</TH><TH>Taken</TH><TH numeric>Size</TH><TH>Encrypted</TH></TR></THead>
                <TBody>
                  {earlier.sets.map((s) => (
                    <TR key={s.name}>
                      <TD truncate className="font-mono text-xs" title={s.name}>{s.name}</TD>
                      <TD nowrap>{s.kind}</TD>
                      <TD nowrap>{formatDate(s.taken)}</TD>
                      <TD numeric>{mb(s.size_bytes)}</TD>
                      <TD nowrap><StatusBadge tone={s.encrypted ? "ok" : "warn"}>{s.encrypted ? "Yes" : "No"}</StatusBadge></TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
            {writable && (
              <>
                <Button variant="danger" disabled={!haveVerifiedNew || !!d?.problems.length} onClick={() => { setEarlierErr(null); setEarlierOpen(true); }}>
                  Remove the earlier backups
                </Button>
                {!haveVerifiedNew && <p className="text-caption text-muted-foreground">Available once a scheduled or manual backup has been taken and proven restorable, so removing the old never leaves less than there was.</p>}
              </>
            )}
          </div>
        )}

      <ConfirmDialog
        open={saveOpen}
        onOpenChange={(v) => { if (!v) setSaveOpen(false); }}
        title="Save the backup settings?"
        description={enabled
          ? "A backup will be taken on this schedule and the newest ones kept. Changing these is recorded against your account."
          : "Scheduled backups will stop. Manual backups, and the backups taken before a restore, a migration or a deployment, are unaffected. Changing these is recorded against your account."}
        confirmLabel="Save"
        error={saveErr}
        requirePassword
        onConfirm={save}
      />
      <ConfirmDialog
        open={runOpen}
        onOpenChange={(v) => { if (!v) setRunOpen(false); }}
        title="Take a backup now?"
        description="It is encrypted and checked like a scheduled one, and counts among the backups kept. It takes a few minutes; clients are not affected."
        confirmLabel="Take a backup"
        error={runErr}
        requirePassword
        onConfirm={run}
      />
      <ConfirmDialog
        open={earlierOpen}
        onOpenChange={(v) => { if (!v) setEarlierOpen(false); }}
        title="Remove the earlier backups?"
        description="This permanently deletes the nightly sets and the older dumps listed above. The backups taken by the one backup system are not touched. This cannot be undone."
        confirmLabel="Remove them"
        confirmVariant="danger"
        confirmText={EARLIER_CONFIRMATION}
        error={earlierErr}
        requirePassword
        onConfirm={removeEarlier}
      />
    </SectionCard>
  );
}
