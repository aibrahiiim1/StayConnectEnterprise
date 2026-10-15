"use client";

// ONE CLIENT DEVICE: what it holds and what staff may do to it (edged /client-devices/{id}, migration 0118).
//
// The actions are deliberately separate, each saying exactly what it changes and what it does not:
//   Disconnect             ends its live sessions. It keeps its places and can sign in again.
//   Release place          on one package: disconnects it there and frees the place for another device.
//   Require fresh sign-in  disconnects it and forgets its remembered sign-in, so the full sign-in page shows.
//   Prepare for a test     all three, for a device reused in a sign-in test.
// None deletes a record, resets a quota, or lets the device take a one-time free package again.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError, type ClientDeviceDetail, type DevicePackage } from "@/lib/api";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody,
} from "@/components/ui/dialog";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Textarea } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { Power, Unlink, KeyRound, FlaskConical } from "lucide-react";
import { formatBytes, formatDate } from "@/lib/utils";

type ActionKind = "disconnect" | "release" | "require-signin" | "prepare-test";

const ACTIONS: Record<ActionKind, { title: string; does: string; keeps: string; confirm: string; reason: boolean }> = {
  disconnect: {
    title: "Disconnect this device",
    does: "Ends the device's live sessions now.",
    keeps: "It keeps its places on its packages and can sign in again straight away.",
    confirm: "Disconnect", reason: false,
  },
  release: {
    title: "Release this device's place",
    does: "Disconnects the device from this package and frees its place, so another device can join.",
    keeps: "The package's remaining time, data and owner do not change, and its history is kept. The device can join again later if a place is free.",
    confirm: "Release place", reason: true,
  },
  "require-signin": {
    title: "Require a fresh sign-in",
    does: "Disconnects the device and forgets its remembered sign-in, so its next visit shows the full sign-in page.",
    keeps: "Its places and packages are unchanged.",
    confirm: "Require sign-in", reason: true,
  },
  "prepare-test": {
    title: "Prepare for a new sign-in test",
    does: "Disconnects the device, frees every place it holds on a live package, and forgets its remembered sign-in — so it reaches the sign-in page as a new visitor would.",
    keeps: "Nothing is deleted and no quota is reset. The device's package history stays, so a free package that may be taken once per device will still be refused on it.",
    confirm: "Prepare device", reason: true,
  },
};

function subject(p: DevicePackage): string {
  switch (p.subject_kind) {
    case "ACCOUNT": return p.subject_label ? `Account ${p.subject_label}` : "Account";
    case "VOUCHER": return "Voucher";
    case "ROOM": return p.subject_label ? `Room ${p.subject_label}` : "Room";
    case "CLIENT": return "Client sign-in (code or social)";
    default: return "Open selection";
  }
}

const LIVE = new Set(["PENDING", "ACTIVE", "SUSPENDED"]);

export function DeviceDetailDialog({ deviceId, mayWrite, onClose, onChanged }: {
  deviceId: string | null;
  mayWrite: boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const toast = useToast();
  const [data, setData] = useState<ClientDeviceDetail | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [pending, setPending] = useState<{ kind: ActionKind; ent?: DevicePackage } | null>(null);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    if (!deviceId) return;
    setErr(null);
    try { setData(await api.get<ClientDeviceDetail>(`/client-devices/${deviceId}`)); } catch (e) { setErr(e); }
  }, [deviceId]);
  useEffect(() => { setData(null); setPending(null); void load(); }, [load]);

  async function run() {
    if (!pending || !deviceId) return;
    setBusy(true); setErr(null);
    try {
      const body: Record<string, string> = {};
      if (ACTIONS[pending.kind].reason) body.reason = reason.trim();
      if (pending.kind === "release" && pending.ent) body.entitlement_id = pending.ent.entitlement_id;
      const r = await api.post<Record<string, number | string>>(`/client-devices/${deviceId}/${pending.kind}`, body);
      const done: Record<ActionKind, string> = {
        disconnect: `${r.disconnected_sessions ?? 0} session(s) ended.`,
        release: "The place is free.",
        "require-signin": "The device will see the sign-in page on its next visit.",
        "prepare-test": `Ready: ${r.places_released ?? 0} place(s) freed, remembered sign-in forgotten.`,
      };
      toast.success(done[pending.kind]);
      setPending(null); setReason("");
      onChanged();
      await load();
    } catch (e) {
      setErr(e instanceof ApiError ? e : e);
    } finally { setBusy(false); }
  }

  const d = data?.device;
  const action = pending ? ACTIONS[pending.kind] : null;
  const reasonOk = !action?.reason || (reason.trim().length >= 4 && reason.trim().length <= 500);

  return (
    <Dialog open={deviceId !== null} onOpenChange={(v) => !v && !busy && onClose()}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle><span className="font-mono">{d?.mac ?? "Device"}</span></DialogTitle>
          <DialogDescription>
            {d ? `Last seen ${d.last_seen ? formatDate(d.last_seen) : "—"}${d.last_ip ? ` at ${d.last_ip}` : ""}.` : "Loading…"}
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-5">
          <ErrorBanner err={err} />
          {!data && !err ? <SkeletonRows rows={3} /> : data && (
            <>
              <section className="flex flex-wrap items-center gap-1.5">
                {data.device.online
                  ? <Badge tone="ok" dot>Online</Badge>
                  : data.device.on_network
                    ? <Badge tone="warn" dot>On the Wi-Fi, not signed in</Badge>
                    : <Badge tone="neutral" dot>Not connected</Badge>}
                {data.device.on_network && (
                  <span className="text-caption text-muted-foreground">
                    {data.device.lease_ip}{data.device.hostname ? ` (${data.device.hostname})` : ""}
                    {data.device.lease_expires ? `, address held until ${formatDate(data.device.lease_expires)}` : ""}
                  </span>
                )}
                {data.restriction.restricted && (
                  <Badge tone="err">Sign-in restricted until {data.restriction.until ? formatDate(data.restriction.until) : "—"}</Badge>
                )}
                {data.remembered_signins > 0 && <Badge tone="info">Remembered sign-in</Badge>}
                {data.free_packages_used > 0 && (
                  <Badge tone="warn">Free package already used on this device</Badge>
                )}
              </section>

              {mayWrite && (
                <section className="flex flex-wrap gap-1.5">
                  {data.device.online && (
                    <Button size="sm" variant="outline" disabled={busy} onClick={() => { setPending({ kind: "disconnect" }); setReason(""); }}>
                      <Power /> Disconnect
                    </Button>
                  )}
                  <Button size="sm" variant="outline" disabled={busy} onClick={() => { setPending({ kind: "require-signin" }); setReason(""); }}>
                    <KeyRound /> Require fresh sign-in
                  </Button>
                  <Button size="sm" variant="outline" disabled={busy} onClick={() => { setPending({ kind: "prepare-test" }); setReason(""); }}>
                    <FlaskConical /> Prepare for a sign-in test
                  </Button>
                </section>
              )}

              {action && (
                <section className="space-y-2 rounded-md border border-border p-3" aria-live="polite">
                  <h3 className="text-sm font-medium">
                    {action.title}{pending?.ent ? ` — ${pending.ent.package_name || "package"}` : ""}
                  </h3>
                  <p className="text-sm">{action.does}</p>
                  <p className="text-caption text-muted-foreground">{action.keeps}</p>
                  {action.reason && (
                    <Field label="Reason" required hint="Recorded with the action, 4–500 characters.">
                      <Textarea value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} />
                    </Field>
                  )}
                  <div className="flex gap-2">
                    <Button size="sm" variant="primary" disabled={busy || !reasonOk} onClick={() => void run()}>
                      {busy ? "Working…" : action.confirm}
                    </Button>
                    <Button size="sm" variant="ghost" disabled={busy} onClick={() => setPending(null)}>Cancel</Button>
                  </div>
                </section>
              )}

              <section className="space-y-2">
                <h3 className="text-sm font-medium">Packages on this device</h3>
                {data.packages.length === 0 ? (
                  <p className="text-sm text-muted-foreground">This device has not used a package.</p>
                ) : data.packages.map((p) => (
                  <div key={p.entitlement_id} className="rounded-md border border-border p-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div>
                        <div className="font-medium">{p.package_name || "Package"}{p.free ? " (free)" : ""}</div>
                        <div className="text-caption text-muted-foreground">{subject(p)}</div>
                      </div>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge tone={LIVE.has(p.status) ? "ok" : "neutral"}>{LIVE.has(p.status) ? "Live" : "Ended"}</Badge>
                        <Badge tone={p.slot === "AUTHORIZED" ? "info" : "neutral"}>
                          {p.slot === "AUTHORIZED" ? "Holds a place" : "Place released"}
                        </Badge>
                      </div>
                    </div>
                    <div className="mt-1 grid grid-cols-1 gap-x-6 text-caption text-muted-foreground sm:grid-cols-3">
                      <span className="tabular">Places {p.places_in_use}{p.max_devices ? ` of ${p.max_devices}` : ""}</span>
                      <span>{LIVE.has(p.status) ? `Ends ${p.window_ends_at ? formatDate(p.window_ends_at) : "—"}` : ""}</span>
                      <span className="tabular">Data {formatBytes(p.consumed_data_bytes)}{p.data_quota_bytes ? ` of ${formatBytes(p.data_quota_bytes)}` : ""}</span>
                    </div>
                    {mayWrite && p.slot === "AUTHORIZED" && LIVE.has(p.status) && (
                      <Button className="mt-2" size="sm" variant="outline" disabled={busy}
                        onClick={() => { setPending({ kind: "release", ent: p }); setReason(""); }}>
                        <Unlink /> Release this place
                      </Button>
                    )}
                  </div>
                ))}
              </section>

              <section className="space-y-2">
                <h3 className="text-sm font-medium">Recent sessions</h3>
                {data.sessions.length === 0 ? (
                  <p className="text-sm text-muted-foreground">No sessions.</p>
                ) : (
                  <Table>
                    <THead><TR><TH>Started</TH><TH>Ended</TH><TH className="hidden sm:table-cell">How</TH><TH className="hidden sm:table-cell">Package</TH></TR></THead>
                    <TBody>
                      {data.sessions.map((x) => (
                        <TR key={x.id}>
                          <TD className="text-sm">{x.started ? formatDate(x.started) : "—"}</TD>
                          <TD className="text-sm">{x.ended ? `${formatDate(x.ended)}${x.end_reason ? ` (${x.end_reason})` : ""}` : <Badge tone="ok" dot>Live</Badge>}</TD>
                          <TD className="hidden text-sm sm:table-cell">{x.method || "—"}</TD>
                          <TD className="hidden text-sm sm:table-cell">{x.package_name || "—"}</TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                )}
              </section>

              {data.actions.length > 0 && (
                <section className="space-y-2">
                  <h3 className="text-sm font-medium">Device actions</h3>
                  <ul className="space-y-1 text-sm">
                    {data.actions.map((a, i) => (
                      <li key={i} className="text-muted-foreground">
                        {formatDate(a.acted_at)} — {a.actor === "STAFF" ? (a.operator || "Staff") : "Client"}: {a.action.toLowerCase().replace("_", " ")} · {a.outcome === "OK" ? "done" : a.outcome.toLowerCase().replaceAll("_", " ")}
                        {a.reason ? ` · “${a.reason}”` : ""}
                      </li>
                    ))}
                  </ul>
                </section>
              )}
            </>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}
