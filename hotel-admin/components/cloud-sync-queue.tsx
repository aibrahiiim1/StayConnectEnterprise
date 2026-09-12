"use client";

// REPORTING TO THE STAYCONNECT CLOUD — the whole queue, in one card, with the two things a property can do
// about it.
//
// WHAT THIS CARD REPLACED. Four bare numbers and a sentence that inferred the problem from the size of the
// queue: "a backlog this size means the queue is not draining — check Cloud connection." On the appliance
// this was written for, the network was fine, the appliance was connected, and the reason nothing drained
// was that the far end had nobody listening. An operator following that sentence would have spent the
// afternoon on the wrong system.
//
// THREE HONEST ADDITIONS.
//
//   * ACCOUNTING. Delivered, waiting, given up on, and the total — so "the backlog has been recovered" is
//     something you can check rather than something somebody says. If the three do not add up to the total,
//     the card says so out loud instead of quietly showing a smaller number.
//   * RETENTION, AND EXACTLY WHAT IT REACHES. It removes DELIVERED records. It is stated in the same breath
//     that it does not touch the ones still waiting or the ones given up on, because a hotel administrator
//     reading "keep for 30 days" beside a queue of 77 000 would reasonably assume the queue is about to be
//     tidied away.
//   * RECOVERY. Records the appliance gave up on were unreachable by any button in the product; the number
//     only ever went up. Recovery returns a bounded batch to the queue, oldest first, and says that the
//     cloud records each one once however many times it arrives.

import { useCallback, useEffect, useState } from "react";
import { CloudUpload, RotateCcw } from "lucide-react";
import { api, CloudSyncSettings, CloudSyncRecovery } from "@/lib/api";
import { describeOutbox, OutboxFigures } from "@/lib/health-words";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm } from "@/components/ui/dialog";
import { formatDate } from "@/lib/utils";

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-1 text-sm">
      <span className="text-muted-foreground">{k}</span>
      <span className="text-right">{v}</span>
    </div>
  );
}

const num = (v?: number | null) => (typeof v === "number" ? v.toLocaleString() : "—");

function bytesLabel(b?: number): string {
  if (typeof b !== "number" || b <= 0) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let v = b;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

export function CloudSyncQueueCard({
  outbox,
  canSetRetention,
  canRecover,
  onChanged,
}: {
  outbox?: OutboxFigures | null;
  canSetRetention: boolean;
  canRecover: boolean;
  onChanged?: () => void;
}) {
  const [settings, setSettings] = useState<CloudSyncSettings | null>(null);
  const [days, setDays] = useState("");
  const [savingSettings, setSavingSettings] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [note, setNote] = useState("");

  const [recoveries, setRecoveries] = useState<CloudSyncRecovery[]>([]);
  const [recoverOpen, setRecoverOpen] = useState(false);
  const [recoverReason, setRecoverReason] = useState("");
  const [recovering, setRecovering] = useState(false);
  const [recoverErr, setRecoverErr] = useState<unknown>(null);

  const words = describeOutbox(outbox);
  const o = outbox ?? { enabled: false };
  const exhausted = o.dead ?? 0;
  const unbalanced = o.balanced === false;

  const load = useCallback(async () => {
    try {
      const s = await api.get<CloudSyncSettings>("/cloud-sync-settings");
      setSettings(s);
      setDays(String(s.delivered_retention_days));
    } catch {
      // A role that may not read the setting still sees the queue. Absence of the card's lower half is the
      // correct outcome, not an error banner about a permission the operator already knows they lack.
      setSettings(null);
    }
    try {
      const r = await api.get<{ recoveries: CloudSyncRecovery[] }>("/cloud-sync-recovery");
      setRecoveries(r.recoveries ?? []);
    } catch {
      setRecoveries([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const lim = settings?.limits ?? { min_days: 1, max_days: 365 };
  const parsed = Number(days);
  const daysBad =
    days.trim() === "" || !Number.isInteger(parsed) || parsed < lim.min_days || parsed > lim.max_days;
  const daysDirty = settings != null && days !== String(settings.delivered_retention_days);

  async function saveRetention() {
    if (daysBad) return;
    setSavingSettings(true);
    setErr(null);
    setNote("");
    try {
      const next = await api.put<CloudSyncSettings>("/cloud-sync-settings", {
        delivered_retention_days: parsed,
      });
      setSettings(next);
      setDays(String(next.delivered_retention_days));
      setNote(
        `Saved. Delivered records are removed after ${next.delivered_retention_days} days. Records still ` +
          `waiting, and records the appliance gave up on, are not affected.`,
      );
    } catch (e) {
      setErr(e);
    } finally {
      setSavingSettings(false);
    }
  }

  async function recover(e: React.FormEvent) {
    e.preventDefault();
    setRecovering(true);
    setRecoverErr(null);
    try {
      const res = await api.post<{ recovered: number; exhausted_remaining: number; note: string }>(
        "/cloud-sync-recovery",
        { reason: recoverReason.trim() },
      );
      setNote(
        `${res.recovered.toLocaleString()} returned to the queue. ${res.exhausted_remaining.toLocaleString()} ` +
          `still to recover. ${res.note}`,
      );
      setRecoverOpen(false);
      setRecoverReason("");
      await load();
      onChanged?.();
    } catch (e2) {
      setRecoverErr(e2);
    } finally {
      setRecovering(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <CloudUpload size={16} /> Reporting to the StayConnect cloud
        </CardTitle>
      </CardHeader>
      <CardBody className="space-y-3">
        <ErrorBanner err={err} />
        {note && (
          <p className="text-sm text-success-subtle-foreground" role="status">
            {note}
          </p>
        )}

        <Row k="In use" v={o.enabled ? "yes" : "no"} />
        <Row k="Delivered" v={num(o.delivered)} />
        <Row
          k="Waiting to be sent"
          v={<b className={(o.pending ?? 0) > 0 ? "text-warning-subtle-foreground" : ""}>{num(o.pending)}</b>}
        />
        <Row
          k="Given up on"
          v={<b className={exhausted > 0 ? "text-destructive" : ""}>{num(exhausted)}</b>}
        />
        <Row k="Total recorded" v={num(o.total)} />
        <Row k="Oldest still waiting" v={o.oldest_pending ? formatDate(o.oldest_pending) : "—"} />
        {exhausted > 0 && (
          <Row k="Oldest given up on" v={o.oldest_exhausted ? formatDate(o.oldest_exhausted) : "—"} />
        )}
        <Row k="Space used" v={bytesLabel(o.bytes)} />

        {unbalanced && (
          // Said out loud rather than absorbed. If the buckets do not add up, a record left the table by
          // some route this product does not know about, and a screen that silently showed the smaller
          // number would be the reason nobody ever found out.
          <Callout tone="warning" title="These numbers do not add up">
            Delivered, waiting and given-up-on together do not equal the total recorded. Something has
            removed records by a route this screen does not know about. Please report this.
          </Callout>
        )}

        <Callout
          tone={
            words.tone === "ok"
              ? "success"
              : words.tone === "err" || words.tone === "warn"
                ? "warning"
                : "neutral"
          }
          title={words.headline}
        >
          {words.summary}
        </Callout>

        {canRecover && exhausted > 0 && (
          <div className="space-y-2 rounded-md border border-border p-3">
            <p className="text-sm">
              <strong>{num(exhausted)}</strong> record{exhausted === 1 ? " was" : "s were"} retried until the
              appliance gave up. They are outside the normal retry cycle and will not be sent again on their
              own.
            </p>
            <p className="text-xs text-muted-foreground">
              Recovery puts them back in the queue, oldest first, in batches. Nothing is deleted and nothing
              is changed — the cloud records each record once, however many times it arrives.
            </p>
            <Button size="sm" onClick={() => { setRecoverOpen(true); setRecoverErr(null); }}>
              <RotateCcw className="mr-1 h-4 w-4" /> Recover these records
            </Button>
          </div>
        )}

        {settings && (
          <div className="space-y-2 border-t border-border pt-3">
            <div className="flex items-center gap-2">
              <label htmlFor="csq-retention" className="text-sm font-medium">
                Keep delivered records for <span className="font-normal text-muted-foreground">(days)</span>
              </label>
              {settings.is_default && <Badge tone="default">Using the standard setting</Badge>}
            </div>
            <div className="flex items-start gap-2">
              <Input
                id="csq-retention"
                type="number"
                inputMode="numeric"
                className={`w-28 ${daysBad ? "border-danger" : ""}`}
                value={days}
                min={lim.min_days}
                max={lim.max_days}
                step={1}
                disabled={!canSetRetention || savingSettings}
                aria-invalid={daysBad}
                aria-describedby="csq-retention-help"
                onChange={(e) => setDays(e.target.value)}
              />
              {canSetRetention && (
                <Button
                  size="sm"
                  onClick={() => void saveRetention()}
                  disabled={!daysDirty || daysBad || savingSettings}
                >
                  {savingSettings ? "Saving…" : "Save"}
                </Button>
              )}
            </div>
            <p id="csq-retention-help" className="text-xs text-muted-foreground">
              Standard: 30. Allowed: {lim.min_days}–{lim.max_days}. This removes records that have{" "}
              <strong>already been delivered</strong> to the cloud. Records still waiting, and records the
              appliance gave up on, are never removed by this — a queue that cannot be delivered is not made
              to look empty.
            </p>
            {daysBad && (
              <p className="text-xs text-danger" role="alert">
                Enter a whole number between {lim.min_days} and {lim.max_days}.
              </p>
            )}
            {!canSetRetention && (
              <p className="text-xs text-muted-foreground">
                Your role can see this setting but not change it.
              </p>
            )}
            {settings.last_change && (
              <p className="text-xs text-muted-foreground">
                Last changed {formatDate(settings.last_change.changed_at)} by{" "}
                {settings.last_change.changed_by}
                {settings.last_change.old_delivered_retention_days != null && (
                  <> — from {settings.last_change.old_delivered_retention_days} days</>
                )}{" "}
                to {settings.last_change.new_delivered_retention_days} days
                {settings.last_change.reason ? ` — ${settings.last_change.reason}` : ""}
              </p>
            )}
          </div>
        )}

        {recoveries.length > 0 && (
          <div className="border-t border-border pt-3">
            <p className="mb-1 text-sm font-medium">Recoveries</p>
            <ul className="space-y-1 text-xs text-muted-foreground">
              {recoveries.slice(0, 5).map((r, i) => (
                <li key={i}>
                  {formatDate(r.requested_at)} — {r.recovered.toLocaleString()} returned by {r.requested_by}
                  {r.exhausted_remaining > 0
                    ? `, ${r.exhausted_remaining.toLocaleString()} still to recover`
                    : ", none left"}
                  {r.reason ? ` — ${r.reason}` : ""}
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardBody>

      <DialogForm
        open={recoverOpen}
        onOpenChange={(v) => {
          if (!v && !recovering) {
            setRecoverOpen(false);
            setRecoverErr(null);
          }
        }}
        title="Recover records the appliance gave up on"
        description="They go back into the queue and are sent in the order they were recorded. Nothing is deleted."
        submitLabel="Recover"
        busy={recovering}
        busyLabel="Recovering…"
        error={recoverErr}
        disabled={recoverReason.trim().length < 3}
        onSubmit={recover}
      >
        <dl className="grid grid-cols-2 gap-2 text-sm">
          <dt className="text-muted-foreground">Records given up on</dt>
          <dd>{num(exhausted)}</dd>
          <dt className="text-muted-foreground">Oldest of them</dt>
          <dd>{o.oldest_exhausted ? formatDate(o.oldest_exhausted) : "—"}</dd>
        </dl>
        <div className="space-y-1">
          <label htmlFor="csq-recover-reason" className="block text-sm font-medium">
            Reason <span className="font-normal text-muted-foreground">(recorded with your name)</span>
          </label>
          <Input
            id="csq-recover-reason"
            value={recoverReason}
            onChange={(e) => setRecoverReason(e.target.value)}
            maxLength={200}
            placeholder="e.g. cloud reporting restored, sending the backlog"
            aria-describedby="csq-recover-help"
          />
          <p id="csq-recover-help" className="text-xs text-muted-foreground">
            At least 3 characters. Recovery happens in batches, so this may need running more than once —
            the card shows how many are left each time.
          </p>
        </div>
        <Callout tone="neutral" title="Arriving twice is safe">
          The cloud records each record once, keyed on this appliance and the record&apos;s own sequence
          number, so a record that is sent again after an interrupted attempt is not counted twice.
        </Callout>
      </DialogForm>
    </Card>
  );
}
