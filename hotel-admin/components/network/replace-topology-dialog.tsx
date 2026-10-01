"use client";

// CHANGE A CLIENT NETWORK'S VLAN OR PORT, SAFELY.
//
// A network's type, port and VLAN cannot be edited in place: the applied configuration is keyed on the network's
// bridge, and changing what that bridge carries underneath it would leave the rendered configuration describing one
// VLAN while the live port carried another. So "move this LAN from an untagged port onto a tagged trunk" is a
// REPLACEMENT: a new network with the new topology that carries everything configured on this one -- addressing,
// DHCP pools, MAC reservations, DNS, portal and isolation settings, and its PMS route -- while this one is disabled
// and kept as history. Nothing changes on the wire until the configuration is applied and confirmed, with the
// usual automatic rollback.

import { useEffect, useState } from "react";
import { api, ApiError, Interface } from "@/lib/api";
import { DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { Callout } from "@/components/ui/error-banner";

const SELECTABLE = new Set(["guest_access", "guest_trunk", "unused"]);

export type ReplaceResult = {
  id: string;
  replaces: string;
  bridge_name: string;
  carried: { pools: number; reservations: number; pms_routes: number };
  packages_limited_to_old_network?: string[] | null;
  active_sessions_on_old_network: number;
};

export function ReplaceTopologyDialog({
  open,
  onOpenChange,
  network,
  onReplaced,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  network: { id: string; name: string; network_type: string; parent_interface: string; vlan_id?: number | null; subnet_cidr: string; gateway_ip: string };
  onReplaced: (r: ReplaceResult) => void;
}) {
  const [interfaces, setInterfaces] = useState<Interface[] | null>(null);
  const [tagged, setTagged] = useState(network.network_type !== "vlan");
  const [parent, setParent] = useState(network.parent_interface);
  const [vlan, setVlan] = useState(network.vlan_id ? String(network.vlan_id) : "");
  const [readdress, setReaddress] = useState(false);
  const [subnet, setSubnet] = useState(network.subnet_cidr);
  const [gateway, setGateway] = useState(network.gateway_ip);
  const [poolStart, setPoolStart] = useState("");
  const [poolEnd, setPoolEnd] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  useEffect(() => {
    if (!open || interfaces) return;
    api.get<{ interfaces: Interface[] }>("/network/interfaces")
      .then((r) => setInterfaces(r.interfaces ?? []))
      .catch(() => setInterfaces([]));
  }, [open, interfaces]);

  const vlanNum = Number(vlan);
  const vlanOk = !tagged || (Number.isInteger(vlanNum) && vlanNum >= 1 && vlanNum <= 4094);
  const same = (tagged ? "vlan" : "untagged") === network.network_type && parent === network.parent_interface &&
    (!tagged || vlanNum === network.vlan_id);
  const problem =
    !parent ? "Choose the port." :
    !vlanOk ? "A VLAN id is between 1 and 4094." :
    same ? "That is the current topology; nothing would change." :
    readdress && (!subnet || !gateway || !poolStart || !poolEnd) ? "A new subnet needs its gateway and DHCP pool." :
    reason.trim().length < 4 ? "Say why (at least 4 characters)." : null;

  async function submit() {
    if (problem) return;
    setBusy(true); setErr(null);
    try {
      const body: Record<string, unknown> = {
        network_type: tagged ? "vlan" : "untagged",
        parent_interface: parent,
        reason: reason.trim(),
        ...(tagged ? { vlan_id: vlanNum } : {}),
        ...(readdress ? { subnet_cidr: subnet, gateway_ip: gateway, pools: [{ start_ip: poolStart, end_ip: poolEnd }] } : {}),
      };
      const r = await api.post<ReplaceResult>(`/network/guest-networks/${network.id}/replace`, body);
      onReplaced(r);
    } catch (e) {
      setErr(e instanceof ApiError ? e : new Error("the network could not be replaced"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      title="Change VLAN or port"
      description={`Creates a replacement for “${network.name}” with the new topology and disables this one.`}
      size="lg"
      submitLabel="Create replacement"
      busy={busy}
      busyLabel="Replacing…"
      error={err}
      disabled={!!problem}
      onSubmit={(e) => { e.preventDefault(); void submit(); }}
    >
      <div className="space-y-4">
        <Callout tone="info" title="Nothing changes until you apply">
          The replacement keeps this network&rsquo;s addressing, DHCP pools, reservations, DNS, sign-in page settings
          and PMS route. After you apply and confirm, clients on this network reconnect on the new one; their internet
          package and data used carry on.
        </Callout>
        <Field label="Port" required>
          <Select aria-label="Port" value={parent} onChange={(e) => setParent(e.target.value)}>
            {(interfaces ?? [{ name: network.parent_interface } as Interface]).map((i) => (
              <option key={i.name} value={i.name} disabled={!!i.role && !SELECTABLE.has(i.role)}>
                {i.name}{i.role ? ` · ${i.role.replace(/_/g, " ")}` : ""}
              </option>
            ))}
          </Select>
        </Field>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" className="size-4 accent-primary" checked={tagged} onChange={(e) => setTagged(e.target.checked)} />
          Tagged VLAN (802.1Q) on a trunk
        </label>
        {tagged && (
          <Field label="VLAN id" required hint="1 to 4094 — the VLAN your switch and wireless controller use for this network." className="max-w-48">
            <Input aria-label="VLAN id" type="number" min={1} max={4094} value={vlan} onChange={(e) => setVlan(e.target.value)} />
          </Field>
        )}
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" className="size-4 accent-primary" checked={readdress} onChange={(e) => setReaddress(e.target.checked)} />
          Also change the subnet (current: <span className="font-mono">{network.subnet_cidr}</span>)
        </label>
        {readdress && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Subnet" required><Input aria-label="New subnet" value={subnet} onChange={(e) => setSubnet(e.target.value)} placeholder="10.30.0.0/24" /></Field>
            <Field label="Gateway" required><Input aria-label="New gateway" value={gateway} onChange={(e) => setGateway(e.target.value)} placeholder="10.30.0.1" /></Field>
            <Field label="DHCP pool start" required><Input aria-label="Pool start" value={poolStart} onChange={(e) => setPoolStart(e.target.value)} placeholder="10.30.0.100" /></Field>
            <Field label="DHCP pool end" required><Input aria-label="Pool end" value={poolEnd} onChange={(e) => setPoolEnd(e.target.value)} placeholder="10.30.0.250" /></Field>
            <p className="text-xs text-muted-foreground sm:col-span-2">MAC reservations are not carried to a new subnet.</p>
          </div>
        )}
        <Field label="Why" required hint="Recorded in the audit log.">
          <Textarea aria-label="Why" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} />
        </Field>
        {problem && <p className="text-xs text-muted-foreground" role="status">{problem}</p>}
      </div>
    </DialogForm>
  );
}
