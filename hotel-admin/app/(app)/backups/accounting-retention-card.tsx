"use client";

// HOW LONG USAGE DETAIL IS KEPT (migration 0122, Product-Owner decision D-H; default 365 days since 0131, GDPR C2).
//
// The appliance records every client session's traffic as a series of small samples. Totals per package and per
// session are kept for good; this setting only decides how long the per-sample DETAIL is kept. The default is
// 365 days; the administrator may choose another number of days, or Unlimited, which means nothing is ever
// removed. A number of days lets the appliance remove, once an hour, detail older than that -- only of sessions that have ended and packages that have finished. Shortening
// it removes history for good, so it asks for a reason and the password, and every change and every removal is
// recorded and listed here.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { formatDate } from "@/lib/utils";
import { useEditFocus } from "@/lib/use-edit-focus";

type PurgeRun = { ran_at: string; retention_days: number; records_deleted: number };
type RetentionResp = {
  settings: { detail_retention_days: number | null };
  changes?: { changed_at: string; changed_by: string; reason: string; old: number | null; new: number | null }[];
  purge_runs?: PurgeRun[];
};

export const RETENTION_MIN_DAYS = 30;
export const RETENTION_MAX_DAYS = 3650;

export function retentionLabel(days: number | null | undefined): string {
  return days == null ? "Unlimited" : `${days} days`;
}

export function AccountingRetentionCard({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [data, setData] = useState<RetentionResp | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [editing, setEditing] = useState(false);
  const { triggerRef, panelRef } = useEditFocus(editing);
  const [unlimited, setUnlimited] = useState(true);
  const [draft, setDraft] = useState("");
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<RetentionResp>("/accounting-retention/");
      // An appliance without the setting (or an answer of another shape) shows nothing rather than a guess.
      if (r && r.settings && "detail_retention_days" in r.settings) setData(r);
      else setUnavailable(true);
    } catch {
      setUnavailable(true);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  if (unavailable) return null;
  const current = data?.settings.detail_retention_days ?? null;
  const n = Number(draft);
  const daysOK = unlimited || (Number.isInteger(n) && n >= RETENTION_MIN_DAYS && n <= RETENTION_MAX_DAYS);
  const valid = daysOK && reason.trim().length >= 4 && password !== "";

  async function save() {
    setBusy(true); setErr(null);
    try {
      await api.put("/accounting-retention/", unlimited
        ? { unlimited: true, reason: reason.trim(), password }
        : { detail_retention_days: n, reason: reason.trim(), password });
      toast.success("Saved.");
      setEditing(false); setPassword(""); setReason("");
      await load();
    } catch (e) {
      setErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
    } finally { setBusy(false); }
  }

  const lastRun = data?.purge_runs?.[0];
  return (
    <Card>
      <CardBody className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <div className="text-emphasis">Usage detail kept for</div>
            <p className="text-caption text-muted-foreground">
              How long the appliance keeps the detailed traffic samples behind each client session. Totals per
              package and per session are always kept. Default 365 days. With a number of days ({RETENTION_MIN_DAYS} to{" "}
              {RETENTION_MAX_DAYS}), detail older than that is removed once an hour, only for sessions that have ended
              and packages that have finished; Unlimited removes nothing. Removed detail cannot be brought back.
            </p>
            {lastRun && (
              <p className="text-caption text-muted-foreground">
                Last removal {formatDate(lastRun.ran_at)}: {lastRun.records_deleted} samples older than{" "}
                {lastRun.retention_days} days.
              </p>
            )}
          </div>
          <div className="flex items-center gap-2">
            <Badge tone="neutral">{data ? retentionLabel(current) : "—"}</Badge>
            {writable && !editing && data && (
              <Button ref={triggerRef} size="sm" variant="outline" onClick={() => {
                setUnlimited(current == null); setDraft(String(current ?? 365)); setEditing(true);
              }}>Change</Button>
            )}
          </div>
        </div>
        <ErrorBanner err={err} />
        {editing && (
          <div ref={panelRef} className="grid gap-3 sm:grid-cols-3">
            <div className="space-y-2 sm:col-span-3">
              <label className="flex items-center gap-2 text-sm">
                <input type="radio" name="accounting-retention" checked={unlimited} onChange={() => setUnlimited(true)} />
                Unlimited (keep all detail)
              </label>
              <label className="flex items-center gap-2 text-sm">
                <input type="radio" name="accounting-retention" checked={!unlimited} onChange={() => setUnlimited(false)} />
                Keep detail for a number of days
              </label>
            </div>
            {!unlimited && (
              <Field label="Days" hint={`${RETENTION_MIN_DAYS} to ${RETENTION_MAX_DAYS}`}>
                <Input type="number" min={RETENTION_MIN_DAYS} max={RETENTION_MAX_DAYS} value={draft}
                  onChange={(e) => setDraft(e.target.value)} />
              </Field>
            )}
            <Field label="Reason" required>
              <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} />
            </Field>
            <Field label="Your password" required hint="Confirms the change.">
              <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <div className="flex gap-2 sm:col-span-3">
              <Button size="sm" variant="primary" disabled={busy || !valid} onClick={() => void save()}>{busy ? "Saving…" : "Save"}</Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing(false)}>Cancel</Button>
            </div>
          </div>
        )}
      </CardBody>
    </Card>
  );
}
