"use client";

// Staff End Package / Reset Package, shared by the Stay, Client Account, session and device views. The server decides
// everything (eligibility, the new window, the devices that may return); this shows exactly what will be ended or reset
// and asks for a reason and the password. A reset is always free: it never charges, posts, refunds or redeems.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { canWrite, canRead } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatBytes, formatDate } from "@/lib/utils";

export type PackageSubject = { kind: "stay" | "account" | "voucher" | "principal" | "session" | "device" | "entitlement"; id: string };

type Row = {
  entitlement_id: string; status: string; terminal_reason?: string | null; package_name: string; end_mode: string;
  activated_at?: string | null; window_ends_at?: string | null; terminated_at?: string | null; purchase_trigger?: string | null;
};

type Preview = {
  entitlement_id: string; status: string; terminal_reason?: string | null; subject_kind: string; subject_id: string;
  stay_id?: string | null; pms_interface_id?: string | null; package_name: string; package_code: string; plan_name: string;
  down_kbps?: number | null; up_kbps?: number | null; max_devices?: number | null; end_mode: string;
  data_quota_bytes?: number | null; bytes_used: number; activated_at?: string | null; window_ends_at?: string | null;
  devices_online: number; sessions_live: number; purchase_trigger?: string | null; paid_minor?: number | null; currency?: string | null;
  superseded_by?: string | null;
  stay?: { status: string; room?: string | null; arrival?: string | null; departure?: string | null; checked_out_at?: string | null; pms_interface?: string | null };
  end: { allowed: boolean; reason?: string | null };
  reset: { allowed: boolean; reason?: string | null; window_ends_at?: string | null; data_quota_bytes?: number | null };
};

type History = { id: string; action: string; outcome: string; reason: string; at: string; sessions: number; devices: number }[];

const SUBJECT_LABEL: Record<string, string> = {
  STAY: "PMS stay", CLIENT_ACCOUNT: "Client account", VOUCHER: "Voucher", CLIENT: "Client", ANONYMOUS: "Device sign-in",
};

function words(code?: string | null): string {
  return code ? code.replace(/^[A-Z_]+: /, "") : "";
}

function newRequestId(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  // Fallback for an old browser: a v4-shaped id from Math.random is enough to tell two clicks of one dialog apart.
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    return (c === "x" ? r : (r & 0x3) | 0x8).toString(16);
  });
}

function speed(p: Preview): string {
  if (!p.down_kbps && !p.up_kbps) return "Unlimited";
  const f = (k?: number | null) => (k ? (k >= 1000 ? `${(k / 1000).toFixed(k % 1000 ? 1 : 0)} Mbit/s` : `${k} kbit/s`) : "—");
  return `${f(p.down_kbps)} down · ${f(p.up_kbps)} up`;
}

export function PackageControls({ subject, onChanged }: { subject: PackageSubject; onChanged?: () => void }) {
  const roles = useOperatorRoles() ?? [];
  const toast = useToast();
  const mayWrite = canWrite("package-controls", roles);
  const mayRead = canRead("package-controls", roles);
  const [rows, setRows] = useState<Row[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [action, setAction] = useState<{ kind: "end" | "reset"; preview: Preview; history: History; requestId: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ packages: Row[] }>(`/package-controls?${subject.kind}=${encodeURIComponent(subject.id)}`);
      setRows(r.packages ?? []); setErr(null);
    } catch (e) { setErr(errMsg(e)); }
  }, [subject.kind, subject.id]);
  useEffect(() => { if (mayRead) void load(); }, [load, mayRead]);

  async function open(kind: "end" | "reset", id: string) {
    setActionErr(null);
    try {
      const r = await api.get<{ package: Preview; history: History }>(`/package-controls/${encodeURIComponent(id)}`);
      setAction({ kind, preview: r.package, history: r.history ?? [], requestId: newRequestId() });
    } catch (e) { setErr(errMsg(e)); }
  }

  async function submitAction({ reason, password }: { reason: string; password: string }) {
    if (!action) return;
    const rule = action.kind === "end" ? action.preview.end : action.preview.reset;
    if (!rule.allowed) { setActionErr(words(rule.reason) || "This package cannot be changed that way."); return; }
    setBusy(true); setActionErr(null);
    const id = action.preview.entitlement_id;
    try {
      if (action.kind === "end") {
        const r = await api.post<{ outcome: string; sessions_ended: number; kernel: { summary: string; withdrawn: number; requested: number } }>(
          `/package-controls/${encodeURIComponent(id)}/end`, { reason, password, request_id: action.requestId });
        if (r.outcome === "ALREADY_ENDED") {
          toast.success("Already ended", "This package had already ended; nothing changed.");
        } else {
          const k = r.kernel;
          toast.success("Package ended",
            `${r.sessions_ended} session${r.sessions_ended === 1 ? "" : "s"} ended. Network access: ` +
            (k.requested === 0 ? "nobody was online." : k.summary === "withdrawn" ? `withdrawn from all ${k.requested}.` :
              `withdrawn from ${k.withdrawn} of ${k.requested}; ${k.summary}.`));
        }
      } else {
        const r = await api.post<{ window_ends_at?: string | null; sessions_rebound: number; devices: number }>(
          `/package-controls/${encodeURIComponent(id)}/reset`, { reason, password, request_id: action.requestId });
        toast.success("Package reset, free of charge",
          `A new cycle started${r.window_ends_at ? `, valid until ${formatDate(r.window_ends_at)}` : " with no end time"}. ` +
          `${r.sessions_rebound} online session${r.sessions_rebound === 1 ? "" : "s"} kept access.`);
      }
      setBusy(false); setAction(null);
      await load();
      onChanged?.();
    } catch (e) {
      setBusy(false);
      setActionErr(e instanceof ApiError ? e.message : e);
    }
  }

  const p = action?.preview;
  const impact = useMemo(() => {
    if (!p || !action) return [];
    if (action.kind === "end") {
      return [
        `The whole package ends now, for every device on it (${p.devices_online} online, ${p.sessions_live} live session${p.sessions_live === 1 ? "" : "s"}).`,
        "Its usage, purchase and payment records are kept unchanged. Nothing is refunded or charged.",
        "A free package is not taken again automatically by this guest (for a room, for the rest of this stay). A paid package can still be bought, and Reset restores this one.",
        "No other guest, stay or PMS connection is affected.",
      ];
    }
    return [
      `A new cycle of “${p.package_name}” starts now: full data allowance (${p.reset.data_quota_bytes ? formatBytes(p.reset.data_quota_bytes) : "unlimited"}), the original speed and device limit` +
        (p.reset.window_ends_at ? `, valid until ${formatDate(p.reset.window_ends_at)}.` : ", with no end time."),
      "Free of charge: no payment, charge, room posting, refund or voucher redemption is made. The original purchase and its usage stay on record.",
      p.status === "ACTIVE"
        ? "Devices online now keep their access on the new cycle without signing in again."
        : "Devices sign in again to use it.",
      ...(p.end_mode === "GRACE_AFTER_CHECKOUT"
        ? ["After Check-out: only the devices that were signed in on this stay before check-out can use it. New devices stay refused."]
        : []),
    ];
  }, [p, action]);

  if (!mayRead) return null;

  return (
    <div className="space-y-2">
      <ErrorBanner err={err} />
      {rows && rows.length === 0 && <p className="text-sm text-muted-foreground">No internet package has been given here yet.</p>}
      {rows && rows.length > 0 && (
        <ul className="divide-y divide-border rounded-md border border-border">
          {rows.map((r) => {
            const live = r.status === "ACTIVE" || r.status === "PENDING" || r.status === "SUSPENDED";
            return (
              <li key={r.entitlement_id} className="flex flex-wrap items-center gap-2 p-2 text-sm">
                <span className="font-medium">{r.package_name}</span>
                <StatusBadge tone={r.status === "ACTIVE" ? "ok" : live ? "warn" : "neutral"}>
                  {r.status === "TERMINATED" ? `Ended${r.terminal_reason ? ` (${r.terminal_reason.toLowerCase()})` : ""}` : r.status.toLowerCase()}
                </StatusBadge>
                {r.purchase_trigger === "ADMIN_GRANT" && <span className="text-2xs text-muted-foreground">reset by staff</span>}
                <span className="text-xs text-muted-foreground">
                  {r.window_ends_at ? `until ${formatDate(r.window_ends_at)}` : r.terminated_at ? `ended ${formatDate(r.terminated_at)}` : "no end time"}
                </span>
                {mayWrite && (
                  <span className="ms-auto flex gap-1">
                    {live && <Button size="sm" variant="danger" onClick={() => void open("end", r.entitlement_id)}>End package</Button>}
                    <Button size="sm" variant="secondary" onClick={() => void open("reset", r.entitlement_id)}>Reset package</Button>
                  </span>
                )}
              </li>
            );
          })}
        </ul>
      )}

      <ConfirmDialog
        open={action !== null}
        onOpenChange={(v) => { if (!v && !busy) setAction(null); }}
        title={action?.kind === "end" ? "End this package?" : "Reset this package?"}
        description={p ? (action?.kind === "end" && !p.end.allowed ? words(p.end.reason)
          : action?.kind === "reset" && !p.reset.allowed ? words(p.reset.reason) : undefined) : undefined}
        confirmLabel={action?.kind === "end" ? "End package" : "Reset package (free)"}
        confirmVariant={action?.kind === "end" ? "danger" : "primary"}
        busy={busy}
        error={actionErr}
        requireReason
        reasonMinLength={4}
        reasonMaxLength={500}
        requirePassword
        consequences={impact}
        onConfirm={submitAction}
      >
        {p && (
          <dl className="grid grid-cols-[auto,1fr] gap-x-3 gap-y-1 text-sm">
            <dt className="text-muted-foreground">Who</dt>
            <dd>{SUBJECT_LABEL[p.subject_kind] ?? p.subject_kind} <span className="font-mono text-2xs text-muted-foreground">{p.subject_id}</span></dd>
            {p.stay && (
              <>
                <dt className="text-muted-foreground">Stay</dt>
                <dd>
                  {p.stay.pms_interface ?? "PMS"} · room {p.stay.room ?? "—"} · {p.stay.arrival ?? "?"} → {p.stay.departure ?? "?"} · {p.stay.status.toLowerCase().replace(/_/g, " ")}
                  {p.stay.checked_out_at ? ` (checked out ${formatDate(p.stay.checked_out_at)})` : ""}
                  <span className="block font-mono text-2xs text-muted-foreground">stay {p.stay_id}</span>
                </dd>
              </>
            )}
            <dt className="text-muted-foreground">Package</dt>
            <dd>{p.package_name} · {p.plan_name} · {speed(p)}{p.max_devices ? ` · up to ${p.max_devices} devices` : ""}</dd>
            <dt className="text-muted-foreground">Usage</dt>
            <dd>{formatBytes(p.bytes_used)}{p.data_quota_bytes ? ` of ${formatBytes(p.data_quota_bytes)}` : " (no data limit)"}</dd>
            <dt className="text-muted-foreground">Ends</dt>
            <dd>{p.status === "TERMINATED" ? `ended (${(p.terminal_reason ?? "").toLowerCase()})` : p.window_ends_at ? formatDate(p.window_ends_at) : "no end time"}</dd>
            <dt className="text-muted-foreground">Devices</dt>
            <dd>{p.devices_online} online · {p.sessions_live} live session{p.sessions_live === 1 ? "" : "s"}</dd>
            {action?.kind === "reset" && (p.paid_minor ?? 0) > 0 && (
              <>
                <dt className="text-muted-foreground">Paid</dt>
                <dd>The original purchase was paid; the reset is not charged and nothing is refunded.</dd>
              </>
            )}
            {action && action.history.length > 0 && (
              <>
                <dt className="text-muted-foreground">Earlier</dt>
                <dd className="text-xs text-muted-foreground">
                  {action.history.slice(0, 3).map((h) => `${h.action.toLowerCase()} ${formatDate(h.at)}: ${h.reason}`).join(" · ")}
                </dd>
              </>
            )}
          </dl>
        )}
      </ConfirmDialog>
    </div>
  );
}
