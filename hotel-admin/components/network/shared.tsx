"use client";

// PIECES SHARED BY THE NETWORKING SCREENS (Guest networks, New guest network, Guest network detail, DHCP & leases,
// Config history). Only those screens import this file.
//
// Everything here is presentation: the words a state is shown with, the list a validation or health result is
// shown as, and the reservation add/edit/remove dialogs that two screens used to hand-build separately (one of
// them behind a native confirm box). The request bodies are exactly the ones those screens sent before.

import * as React from "react";
import { api, Whoami, ValidationIssue, HealthCheck, Reservation, GuestNetwork } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/misc";
import { cn } from "@/lib/utils";

/* ------------------------------------------------------------------------------------------------------ */
/* Roles                                                                                                    */
/* ------------------------------------------------------------------------------------------------------ */

/**
 * The operator's roles, and whether they are known yet. `known` matters: before whoami answers, the page must
 * neither offer write actions nor tell the operator they are read-only.
 */
export function useNetworkAccess(): { roles: string[]; known: boolean; writable: boolean } {
  const [roles, setRoles] = React.useState<string[] | null>(null);
  React.useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const list = roles ?? [];
  return { roles: list, known: roles !== null, writable: canWrite("network", list) };
}

/* ------------------------------------------------------------------------------------------------------ */
/* Words for states                                                                                         */
/* ------------------------------------------------------------------------------------------------------ */

export const REVISION_STATE: Record<string, { label: string; tone: "ok" | "warn" | "err" | "default" }> = {
  active: { label: "Active", tone: "ok" },
  pending_confirmation: { label: "Pending confirmation", tone: "warn" },
  rolled_back: { label: "Rolled back", tone: "err" },
  failed: { label: "Failed", tone: "err" },
  superseded: { label: "Superseded", tone: "default" },
};

export function RevisionStateBadge({ state }: { state: string }) {
  const s = REVISION_STATE[state] ?? { label: state.replace(/_/g, " "), tone: "default" as const };
  return <Badge tone={s.tone} dot={state === "pending_confirmation"}>{s.label}</Badge>;
}

export const DHCP_MODE: Record<string, { label: string; tone: "ok" | "warn" | "err" | "info" | "default" }> = {
  local: { label: "Local", tone: "ok" },
  relay: { label: "Relay", tone: "info" },
  external: { label: "External", tone: "warn" },
  disabled: { label: "Disabled", tone: "err" },
};

export function DhcpModeBadge({ mode }: { mode: string }) {
  const m = DHCP_MODE[mode] ?? { label: mode, tone: "default" as const };
  return <Badge tone={m.tone}>{m.label}</Badge>;
}

export function networkTypeLabel(n: Pick<GuestNetwork, "network_type" | "vlan_id">): string {
  return n.network_type === "vlan" ? `VLAN ${n.vlan_id ?? "?"}` : "Untagged";
}

/* ------------------------------------------------------------------------------------------------------ */
/* Validation issues and health checks                                                                      */
/* ------------------------------------------------------------------------------------------------------ */

export function ValidationIssueList({ issues, className }: { issues: ValidationIssue[]; className?: string }) {
  if (issues.length === 0) return null;
  return (
    <ul className={cn("space-y-1.5", className)}>
      {issues.map((i, k) => (
        <li key={k} className="text-sm">
          <span className="font-mono text-xs">{i.field}</span>
          <span className="text-muted-foreground"> — </span>
          {i.message}
          <span className="ms-1 text-caption text-muted-foreground">({i.code})</span>
        </li>
      ))}
    </ul>
  );
}

export function HealthCheckList({
  checks,
  className,
}: {
  checks: { name: string; ok: boolean; detail?: string; at?: string }[];
  className?: string;
}) {
  if (checks.length === 0) return null;
  return (
    <ul className={cn("divide-y divide-border rounded-md border border-border", className)}>
      {checks.map((h, k) => (
        <li key={k} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-sm">
          <Badge tone={h.ok ? "ok" : "err"}>{h.ok ? "Passed" : "Failed"}</Badge>
          <span className="font-mono text-xs">{h.name}</span>
          {h.detail && <span className="min-w-0 break-words text-caption text-muted-foreground">{h.detail}</span>}
          {h.at && <span className="ms-auto text-caption tabular text-muted-foreground">{h.at}</span>}
        </li>
      ))}
    </ul>
  );
}

/** The "what did Validate / Apply find" block, shown under the pending banner or on its own. */
export function ApplyResults({
  validation,
  health,
}: {
  validation: { ok: boolean; issues?: ValidationIssue[] } | null;
  health: HealthCheck[] | null;
}) {
  if (!validation && !(health && health.length)) return null;
  return (
    <div className="space-y-4">
      {validation && (
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <span className="text-label">Validation</span>
            <Badge tone={validation.ok ? "ok" : "err"}>{validation.ok ? "Passed" : "Issues found"}</Badge>
          </div>
          {validation.issues && validation.issues.length > 0 && <ValidationIssueList issues={validation.issues} />}
        </div>
      )}
      {health && health.length > 0 && (
        <div className="space-y-2">
          <div className="text-label">Health checks</div>
          <HealthCheckList checks={health} />
        </div>
      )}
    </div>
  );
}

/* ------------------------------------------------------------------------------------------------------ */
/* DHCP reservations: add, edit, remove                                                                     */
/* ------------------------------------------------------------------------------------------------------ */

type NewReservation = { guest_network_id: string; mac: string; reserved_ip: string; hostname: string; enabled: boolean };

/**
 * Add a reservation. `networkId` fixed = the detail page (one network); otherwise the operator picks the network
 * from `networks`. The request body is the one both screens sent before.
 */
export function AddReservationDialog({
  open,
  onOpenChange,
  networkId,
  networks,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  networkId?: string;
  networks?: GuestNetwork[];
  onSaved: () => void;
}) {
  const blank: NewReservation = { guest_network_id: networkId ?? "", mac: "", reserved_ip: "", hostname: "", enabled: true };
  const [f, setF] = React.useState<NewReservation>(blank);
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<string | null>(null);

  React.useEffect(() => {
    if (open) { setF({ ...blank }); setErr(null); }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, networkId]);

  async function submit() {
    const gid = networkId ?? f.guest_network_id;
    if (!gid || !f.mac.trim() || !f.reserved_ip.trim()) {
      setErr(networkId ? "MAC and reserved IP are required." : "Guest network, MAC and reserved IP are required.");
      return;
    }
    setBusy(true); setErr(null);
    try {
      await api.post("/network/dhcp/reservations", {
        guest_network_id: gid, mac: f.mac.trim(), reserved_ip: f.reserved_ip.trim(),
        hostname: f.hostname.trim() || undefined, enabled: f.enabled,
      });
      onOpenChange(false);
      onSaved();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      title="New reservation"
      description="Pin a device to a fixed address. The device gets this address the next time it asks for one."
      submitLabel="Add reservation"
      busyLabel="Adding…"
      busy={busy}
      error={err}
      onSubmit={submit}
    >
      {!networkId && (
        <Field label="Guest network" required>
          <Select value={f.guest_network_id} onChange={(e) => setF({ ...f, guest_network_id: e.target.value })}>
            <option value="">Choose a guest network…</option>
            {(networks ?? []).map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
          </Select>
        </Field>
      )}
      <Field label="Device MAC address" required>
        <Input value={f.mac} onChange={(e) => setF({ ...f, mac: e.target.value })} placeholder="aa:bb:cc:dd:ee:ff" className="font-mono" autoComplete="off" />
      </Field>
      <Field label="Reserved IP address" required hint="Must be inside the guest network's subnet.">
        <Input value={f.reserved_ip} onChange={(e) => setF({ ...f, reserved_ip: e.target.value })} placeholder="10.20.0.50" className="font-mono" autoComplete="off" />
      </Field>
      <Field label="Hostname" hint="Optional. A name that helps you recognise the device.">
        <Input value={f.hostname} onChange={(e) => setF({ ...f, hostname: e.target.value })} placeholder="lobby-printer" />
      </Field>
      <SwitchRow label="Enabled" checked={f.enabled} onChange={(v) => setF({ ...f, enabled: v })} />
    </DialogForm>
  );
}

/** Edit a reservation: the MAC and network are fixed, the address, hostname and on/off can change. */
export function EditReservationDialog({
  reservation,
  networkName,
  onClose,
  onSaved,
}: {
  reservation: Reservation | null;
  networkName?: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [r, setR] = React.useState<Reservation | null>(reservation);
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<string | null>(null);
  React.useEffect(() => { setR(reservation); setErr(null); }, [reservation]);

  async function submit() {
    if (!r) return;
    setBusy(true); setErr(null);
    try {
      await api.put(`/network/dhcp/reservations/${r.id}`, {
        reserved_ip: r.reserved_ip, hostname: r.hostname ?? "", enabled: r.enabled,
      });
      onClose();
      onSaved();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  return (
    <DialogForm
      open={reservation !== null}
      onOpenChange={(v) => !v && onClose()}
      title="Edit reservation"
      submitLabel="Save"
      busyLabel="Saving…"
      busy={busy}
      error={err}
      onSubmit={submit}
    >
      {r && (
        <>
          {networkName !== undefined && (
            <Field label="Guest network"><Input value={networkName} disabled /></Field>
          )}
          <Field label="Device MAC address" hint="The device cannot change. Remove the reservation and add a new one instead.">
            <Input value={r.mac} disabled className="font-mono" />
          </Field>
          <Field label="Reserved IP address" required>
            <Input value={r.reserved_ip} onChange={(e) => setR({ ...r, reserved_ip: e.target.value })} className="font-mono" autoComplete="off" />
          </Field>
          <Field label="Hostname">
            <Input value={r.hostname ?? ""} onChange={(e) => setR({ ...r, hostname: e.target.value })} />
          </Field>
          <SwitchRow label="Enabled" checked={r.enabled} onChange={(v) => setR({ ...r, enabled: v })} />
        </>
      )}
    </DialogForm>
  );
}

/**
 * Remove a reservation. This replaced a native confirm box on two screens. It says which address stops being
 * reserved and what that means for the device — a printer, a TV or a door lock that relied on it.
 */
export function RemoveReservationDialog({
  reservation,
  onClose,
  onRemoved,
}: {
  reservation: Reservation | null;
  onClose: () => void;
  onRemoved: () => void;
}) {
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<string | null>(null);
  React.useEffect(() => { setErr(null); }, [reservation]);
  const r = reservation;
  return (
    <ConfirmDialog
      open={r !== null}
      onOpenChange={(v) => !v && onClose()}
      title={r ? `Remove the reserved address ${r.reserved_ip}?` : "Remove the reserved address?"}
      description={r?.hostname ? `Reserved for ${r.hostname} (${r.mac}).` : r ? `Reserved for ${r.mac}.` : undefined}
      consequences={[
        "That device will be given any free address next time it connects.",
        "Anything that reaches the device by this address (a printer queue, a TV controller) may stop finding it.",
      ]}
      confirmLabel="Remove reservation"
      confirmVariant="danger"
      busy={busy}
      error={err}
      onConfirm={async () => {
        if (!r) return;
        setBusy(true); setErr(null);
        try {
          await api.del(`/network/dhcp/reservations/${r.id}`);
          onClose();
          onRemoved();
        } catch (e) { setErr(errMsg(e)); }
        finally { setBusy(false); }
      }}
    />
  );
}

/* ------------------------------------------------------------------------------------------------------ */
/* Small form helpers                                                                                       */
/* ------------------------------------------------------------------------------------------------------ */

/** A labelled switch row, with an optional one-line explanation. The label is bound to the switch. */
export function SwitchRow({
  label,
  hint,
  checked,
  onChange,
  disabled,
}: {
  label: React.ReactNode;
  hint?: React.ReactNode;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
}) {
  const id = React.useId();
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0 space-y-0.5">
        <label htmlFor={id} className="text-label">{label}</label>
        {hint && <p className="text-caption text-muted-foreground">{hint}</p>}
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onChange} disabled={disabled} />
    </div>
  );
}
