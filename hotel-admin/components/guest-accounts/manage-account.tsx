"use client";

// Accounts with history are removed from use; accounts without history can be deleted.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError, type GuestAccount, type GuestAccountManage } from "@/lib/api";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, ConfirmDialog,
} from "@/components/ui/dialog";
import { REASON_MAX_CHARS, REASON_MIN_CHARS } from "@/lib/reason";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { BadgeCheck, RotateCcw, Send, Power, Trash2 } from "lucide-react";
import { formatBytes, formatDate } from "@/lib/utils";
import { verificationStatus } from "@/lib/account-verification";
import { DeviceDetailDialog } from "@/components/devices/device-detail";

const LIVE = new Set(["PENDING", "ACTIVE", "SUSPENDED"]);

function packageTone(status: string): "ok" | "warn" | "err" | "neutral" {
  if (status === "ACTIVE") return "ok";
  if (status === "PENDING" || status === "SUSPENDED") return "warn";
  return "neutral";
}

function packageLabel(status: string): string {
  switch (status) {
    case "ACTIVE": return "Active";
    case "PENDING": return "Not started";
    case "SUSPENDED": return "Suspended";
    case "TERMINATED": return "Ended";
    default: return status;
  }
}

export function ManageAccountDialog({
  account, mayWrite, mayReadDevices = true, mayActOnDevices, mayVouch = false, onClose, onChanged, onRemove,
}: {
  account: GuestAccount | null;
  mayWrite: boolean;
  /** Hide the Device action without sessions-read permission. */
  mayReadDevices?: boolean;
  /** Device actions require sessions-write, separate from guest-accounts-write. */
  mayActOnDevices?: boolean;
  /** Staff verification requires code-reveal-write, password, and reason. */
  mayVouch?: boolean;
  onClose: () => void;
  onChanged: () => void;
  onRemove: (a: GuestAccount, removal: GuestAccountManage["removal"]) => void;
}) {
  const toast = useToast();
  const [data, setData] = useState<GuestAccountManage | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [device, setDevice] = useState<string | null>(null);
  const [vouching, setVouching] = useState(false);
  const [vouchErr, setVouchErr] = useState<unknown>(null);
  const deviceActions = mayActOnDevices ?? mayWrite;

  const load = useCallback(async () => {
    if (!account) return;
    setErr(null);
    try {
      setData(await api.get<GuestAccountManage>(`/guest-accounts/${account.id}/manage`));
    } catch (e) { setErr(e); }
  }, [account]);
  useEffect(() => { setData(null); void load(); }, [load]);

  async function act(key: string, fn: () => Promise<unknown>, done: string) {
    setBusy(key); setErr(null);
    try {
      await fn();
      toast.success(done);
      onChanged();
      await load();
    } catch (e) {
      if (e instanceof ApiError && e.code === "resend_wait") {
        const wait = Number(e.body?.retry_after_seconds);
        toast.error("Too soon to send another email.", wait > 0 ? `Try again in ${wait} seconds.` : undefined);
      } else setErr(e);
    } finally { setBusy(null); }
  }

  const a = data?.account ?? account;
  const verification = a ? verificationStatus(a) : null;
  const removed = data?.removed ?? false;
  const selfService = !!a?.self_registered;
  const canAdjust = mayWrite && selfService && !removed && !a?.email_verified;

  return (
    <Dialog open={account !== null} onOpenChange={(v) => !v && !busy && onClose()}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>Manage {a?.username}</DialogTitle>
          <DialogDescription>
            {removed
              ? "This account was removed. Its history is kept; it can no longer sign in."
              : "Verification, packages and devices of this account."}
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-5">
          <ErrorBanner err={err} />

          <section className="space-y-2">
            <div className="flex flex-wrap items-center gap-1.5">
              {selfService && <Badge tone="info">Self-service</Badge>}
              {removed
                ? <Badge tone="neutral">Removed</Badge>
                : <Badge tone={a?.enabled ? "ok" : "err"} dot>{a?.enabled ? "Can sign in" : "Disabled"}</Badge>}
              {verification && !removed && <Badge tone={verification.tone}>{verification.label}</Badge>}
            </div>
            {selfService && !removed && (
              <dl className="grid grid-cols-1 gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
                <div><dt className="text-muted-foreground">Email verified</dt>
                  <dd>{a?.email_verified_at ? formatDate(a.email_verified_at) : "Not yet"}</dd></div>
                <div><dt className="text-muted-foreground">Verification deadline</dt>
                  <dd>{a?.email_verified ? "—" : a?.verification_deadline ? formatDate(a.verification_deadline) : "None"}</dd></div>
                <div><dt className="text-muted-foreground">Extra hour used</dt>
                  <dd className="tabular">{a?.grace_extensions_used ?? 0}{a?.grace_until ? ` (until ${formatDate(a.grace_until)})` : ""}</dd></div>
                <div><dt className="text-muted-foreground">Sign-ins</dt>
                  <dd className="tabular">{a?.login_count ?? 0}</dd></div>
              </dl>
            )}
            {mayWrite && !removed && (
              <div className="flex flex-wrap gap-1.5 pt-1">
                {canAdjust && (
                  <>
                    {mayVouch && (
                      <Button size="sm" variant="outline" disabled={busy !== null}
                        onClick={() => { setVouchErr(null); setVouching(true); }}>
                        <BadgeCheck /> {busy === "verify" ? "Saving…" : "Mark email verified"}
                      </Button>
                    )}
                    <Button size="sm" variant="outline" disabled={busy !== null}
                      onClick={() => void act("restart", () => api.post(`/guest-accounts/${a!.id}/restart-verification`),
                        "A new verification period has started.")}>
                      <RotateCcw /> {busy === "restart" ? "Saving…" : "New verification period"}
                    </Button>
                    <Button size="sm" variant="outline" disabled={busy !== null}
                      onClick={() => void act("resend", () => api.post(`/guest-accounts/${a!.id}/resend-verification`),
                        "Verification email queued.")}>
                      <Send /> {busy === "resend" ? "Sending…" : "Resend verification email"}
                    </Button>
                  </>
                )}
                {(a?.active_devices ?? 0) > 0 && (
                  <Button size="sm" variant="outline" disabled={busy !== null}
                    onClick={() => void act("disconnect", () => api.post(`/guest-accounts/${a!.id}/disconnect`),
                      "The account's devices were disconnected.")}>
                    <Power /> {busy === "disconnect" ? "Disconnecting…" : "Disconnect devices"}
                  </Button>
                )}
                <Button size="sm" variant="outline" className="text-destructive hover:text-destructive"
                  disabled={busy !== null || !data}
                  onClick={() => data && onRemove(data.account, data.removal)}>
                  <Trash2 /> Remove account
                </Button>
              </div>
            )}
            {canAdjust && (
              <p className="text-caption text-muted-foreground">
                {mayVouch ? "Mark email verified when you have confirmed the address yourself; it asks for your password and a reason. " : ""}A new verification period moves
                the deadline forward by the site&apos;s setting and makes the extra hour available again. Neither
                changes the client&apos;s package.
              </p>
            )}
          </section>

          <section className="space-y-2">
            <h3 className="text-sm font-medium">Packages</h3>
            {!data && !err ? (
              <SkeletonRows rows={2} />
            ) : data && data.entitlements.length === 0 ? (
              <p className="text-sm text-muted-foreground">This account has not taken a package.</p>
            ) : (
              data?.entitlements.map((e) => (
                <div key={e.id} className="rounded-md border border-border p-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="font-medium">{e.package_name || "Package"}</div>
                    <Badge tone={packageTone(e.status)}>{packageLabel(e.status)}</Badge>
                  </div>
                  <div className="mt-1 grid grid-cols-1 gap-x-6 text-caption text-muted-foreground sm:grid-cols-3">
                    <span>Started {e.activated_at ? formatDate(e.activated_at) : "—"}</span>
                    <span>
                      {LIVE.has(e.status)
                        ? `Ends ${e.window_ends_at ? formatDate(e.window_ends_at) : "—"}`
                        : `Ended ${e.terminated_at ? formatDate(e.terminated_at) : "—"}`}
                    </span>
                    <span className="tabular">
                      Data {formatBytes(e.consumed_data_bytes)}
                      {e.data_quota_bytes ? ` of ${formatBytes(e.data_quota_bytes)}` : ""}
                    </span>
                  </div>
                  {e.devices.length > 0 && (
                    <Table className="mt-2">
                      <THead>
                        <TR>
                          <TH>Device</TH>
                          <TH>Slot</TH>
                          <TH className="hidden sm:table-cell">First used</TH>
                          <TH><span className="sr-only">Actions</span></TH>
                        </TR>
                      </THead>
                      <TBody>
                        {e.devices.map((d) => (
                          <TR key={d.mac}>
                            <TD className="font-mono text-sm">{d.mac}</TD>
                            <TD>
                              {d.online
                                ? <Badge tone="ok" dot>Online</Badge>
                                : d.status === "AUTHORIZED"
                                  ? <Badge tone="neutral">Holds a slot</Badge>
                                  : <Badge tone="neutral">Released</Badge>}
                            </TD>
                            <TD className="hidden text-sm text-muted-foreground sm:table-cell">
                              {d.first_authorized ? formatDate(d.first_authorized) : "—"}
                            </TD>
                            <TD className="text-end">
                              {mayReadDevices && (
                                <Button size="sm" variant="ghost" onClick={() => setDevice(d.id)}>Device</Button>
                              )}
                            </TD>
                          </TR>
                        ))}
                      </TBody>
                    </Table>
                  )}
                  {LIVE.has(e.status) && e.max_devices ? (
                    <p className="mt-1 text-caption text-muted-foreground">
                      This package allows {e.max_devices} device{e.max_devices === 1 ? "" : "s"}. A device keeps its
                      place after it signs out. The client can replace a device on the portal; staff can release a
                      place from the device&apos;s view.
                    </p>
                  ) : null}
                </div>
              ))
            )}
            {data && data.entitlements_total > data.entitlements.length && (
              <p className="text-caption text-muted-foreground">
                Showing the {data.entitlements.length} most recent of {data.entitlements_total} packages.
              </p>
            )}
          </section>
        </DialogBody>
      </DialogContent>
      <DeviceDetailDialog deviceId={device} mayWrite={deviceActions} onClose={() => setDevice(null)}
        onChanged={() => { onChanged(); void load(); }} />
      <ConfirmDialog
        open={vouching}
        onOpenChange={(v) => !v && setVouching(false)}
        title="Mark this email address verified?"
        description={a ? `You vouch that ${a.username} belongs to this client. The account's verification deadline no longer applies.` : undefined}
        consequences={["The client was not asked to prove the address; your name and reason are recorded instead."]}
        confirmLabel="Mark verified"
        requireReason
        reasonMinLength={REASON_MIN_CHARS}
        reasonMaxLength={REASON_MAX_CHARS}
        reasonPlaceholder="For example: the client showed the email on their phone at reception"
        requirePassword
        busy={busy === "verify"}
        error={vouchErr}
        onConfirm={async ({ reason, password }) => {
          setBusy("verify"); setVouchErr(null);
          try {
            await api.post(`/guest-accounts/${a!.id}/verify-email`, { reason: reason.trim(), password });
            toast.success("The email address is marked verified.");
            setVouching(false);
            onChanged();
            await load();
          } catch (e) {
            setVouchErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
          } finally { setBusy(null); }
        }}
      />
    </Dialog>
  );
}
