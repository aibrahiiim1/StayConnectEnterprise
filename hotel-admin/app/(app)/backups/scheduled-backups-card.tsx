"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type BackupSettings } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Switch } from "@/components/ui/misc";
import { SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";
import { DatabaseBackup } from "lucide-react";
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
  const outOfRange = fields.some((x) => { const v = valueOf(x.key); return !Number.isFinite(v) || v < x.min || v > x.max; });
  const enabled = valueOf("BACKUP_ENABLED") === 1;

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
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><DatabaseBackup className="size-4" aria-hidden /> Scheduled backups</CardTitle>
        <CardDescription>
          One backup system: the database and the configuration and keys, encrypted to the backup key holders and loaded into a scratch
          database to prove they restore. Scheduled backups are off until you switch them on.
        </CardDescription>
      </CardHeader>
      <CardBody className="space-y-4">
        <ErrorBanner err={err} />
        <Callout tone={head.tone === "ok" ? "success" : head.tone === "err" ? "danger" : "warning"} title={head.title}>{head.detail}</Callout>

        {d && (
          <p className="text-sm text-muted-foreground">
            {d.schedule.next ? <>Next scheduled backup: <strong>{formatDate(d.schedule.next)}</strong>. </> : null}
            {d.last_error ? <>The last attempt failed: {d.last_error}. </> : null}
            The backups taken before a restore, a migration or a deployment are always taken, whatever the schedule says.
          </p>
        )}

        {settings && fields.length > 0 && (
          <div className="space-y-4 rounded-md border border-border p-4">
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
            <div className="grid gap-5 sm:grid-cols-2">
              {fields.filter((x) => x.key !== "BACKUP_ENABLED").map((x) => (
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
            {writable && dirty && (
              <div className="flex flex-wrap gap-2">
                <Button disabled={outOfRange} onClick={() => { setSaveErr(null); setSaveOpen(true); }}>Save backup settings</Button>
                <Button variant="ghost" onClick={() => setEdits(Object.fromEntries(fields.map((x) => [x.key, String(x.value)])))}>Discard changes</Button>
              </div>
            )}
          </div>
        )}

        {d && d.backups.length > 0 && (
          <div className="overflow-x-auto">
            <Table>
              <THead><TR><TH>Taken</TH><TH>Kind</TH><TH>Size</TH><TH>Proven restorable</TH><TH>Ledger entries it holds</TH></TR></THead>
              <TBody>
                {d.backups.map((b) => (
                  <TR key={b.name}>
                    <TD>{formatDate(b.taken_at)}</TD>
                    <TD>
                      <Badge tone={b.kind === "scheduled" || b.kind === "manual" ? "info" : "default"}>{backupKindLabel(b.kind)}</Badge>
                      {(b.reason || b.by) && <div className="text-2xs text-muted-foreground">{[b.reason, b.by && `by ${b.by}`].filter(Boolean).join(" · ")}</div>}
                    </TD>
                    <TD>{mb(b.size_bytes)}</TD>
                    <TD><Badge tone={b.restorable ? "ok" : "err"}>{b.restorable ? "yes" : "no"}</Badge></TD>
                    <TD>{b.ledger_seq}</TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </div>
        )}

        {d?.problems && d.problems.length > 0 && (
          <Callout tone="danger" title="A stored backup changed">{d.problems.map((p) => `${p.name}: ${p.detail}`).join("; ")}</Callout>
        )}

        {writable && (
          <Button variant="outline" disabled={!!d?.running || d?.state === "needs_key_holder"} onClick={() => { setRunErr(null); setRunOpen(true); }}>
            {d?.running ? "A backup is running…" : "Take a backup now"}
          </Button>
        )}

        {earlier && earlier.count > 0 && (
          <div className="space-y-3 rounded-md border border-border p-4">
            <div>
              <p className="text-emphasis">Earlier backups ({earlier.count}, {mb(earlier.size_bytes)})</p>
              <p className="text-sm text-muted-foreground">
                These were made before there was one backup system: the nightly sets and older on-demand dumps. They are kept exactly as they
                were and are never removed automatically. Nightly sets are restored by hand from the support runbook; an earlier dump that was
                verified can be restored from the list below.
                {earlier.unencrypted > 0 ? ` ${earlier.unencrypted} of them are not encrypted.` : ""}
              </p>
            </div>
            <div className="overflow-x-auto">
              <Table>
                <THead><TR><TH>Name</TH><TH>Kind</TH><TH>Taken</TH><TH>Size</TH><TH>Encrypted</TH></TR></THead>
                <TBody>
                  {earlier.sets.map((s) => (
                    <TR key={s.name}>
                      <TD className="font-mono text-xs">{s.name}</TD>
                      <TD>{s.kind}</TD>
                      <TD>{formatDate(s.taken)}</TD>
                      <TD>{mb(s.size_bytes)}</TD>
                      <TD><Badge tone={s.encrypted ? "ok" : "warn"}>{s.encrypted ? "yes" : "no"}</Badge></TD>
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
      </CardBody>

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
    </Card>
  );
}
