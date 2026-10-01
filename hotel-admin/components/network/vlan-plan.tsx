"use client";

// ONE TRUNK, SEVERAL CLIENT NETWORKS.
//
// A tagged port can carry the client VLANs of two hotel extensions, or a staff VLAN beside the guest one. Each
// VLAN is its OWN client network: its own subnet, its own gateway and its own DHCP scope, so two VLANs never
// share an address range or a broadcast domain. OneGate serves them all from one local DHCP service; that is
// one service with several scopes, never one scope shared between VLANs.
//
// What OneGate configures is its own LAN side. The switch port must be a trunk carrying these VLAN ids, and the
// wireless controller must tag each SSID onto the matching VLAN — neither is configured from here, so the
// review step says so plainly rather than implying OneGate has done it.

import { Field, Input, Select } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Trash2, Plus, ChevronDown, ChevronRight } from "lucide-react";
import { addressingFor, suggestSubnet } from "@/lib/network-trunk";

export type VlanRow = {
  key: number;
  vlan: string;
  name: string;
  subnet: string;
  gateway: string;
  poolStart: string;
  poolEnd: string;
  /** The operator edited the addressing, so a VLAN change must not overwrite it. */
  touched: boolean;
  /** Per-VLAN DHCP/DNS, used only in "separate" mode. */
  dnsMode: "appliance" | "custom";
  dnsServers: string;
  domainName: string;
  leaseDefault: string;
  leaseMin: string;
  leaseMax: string;
  open: boolean;
};

let nextKey = 1;
export function newVlanRow(defaults?: Partial<VlanRow>): VlanRow {
  return {
    key: nextKey++, vlan: "", name: "", subnet: "", gateway: "", poolStart: "", poolEnd: "", touched: false,
    dnsMode: "appliance", dnsServers: "", domainName: "guest.local",
    leaseDefault: "3600", leaseMin: "900", leaseMax: "7200", open: false,
    ...defaults,
  };
}

/** Fill a row's addressing from a free /24, unless the operator has already chosen it. */
export function withSuggestedAddressing(row: VlanRow, used: Set<string>): VlanRow {
  const v = Number(row.vlan);
  if (row.touched || !Number.isInteger(v) || v < 1 || v > 4094) return row;
  const subnet = suggestSubnet(v, used);
  return { ...row, subnet, ...addressingFor(subnet), name: row.name || `VLAN ${v}` };
}

/** Everything wrong with the plan, by row key, plus a general message. Empty means it can be created. */
export function vlanPlanProblems(
  rows: VlanRow[],
  usedVLANs: Set<number>,
  usedSubnets: Set<string>,
): Record<number, string> {
  const out: Record<number, string> = {};
  const seenVLAN = new Map<number, number>();
  const seenSubnet = new Map<string, number>();
  for (const r of rows) {
    const v = Number(r.vlan);
    if (!Number.isInteger(v) || v < 1 || v > 4094) out[r.key] = "A VLAN id is 1 to 4094.";
    else if (usedVLANs.has(v)) out[r.key] = `VLAN ${v} is already on this port.`;
    else if (seenVLAN.has(v)) out[r.key] = `VLAN ${v} is listed twice.`;
    else if (!r.name.trim()) out[r.key] = "Name it.";
    else if (!r.subnet || !r.gateway || !r.poolStart || !r.poolEnd) out[r.key] = "Subnet, gateway and pool are needed.";
    else if (usedSubnets.has(r.subnet)) out[r.key] = "That subnet is already used by another client network.";
    else if (seenSubnet.has(r.subnet)) out[r.key] = "Each VLAN needs its own subnet.";
    else if (r.dnsMode === "custom" && !r.dnsServers.trim()) out[r.key] = "Give at least one DNS server.";
    seenVLAN.set(v, r.key);
    seenSubnet.set(r.subnet, r.key);
  }
  return out;
}

/** The VLAN ids carried on the chosen trunk. */
export function VlanIdList({
  rows, onChange, problems,
}: { rows: VlanRow[]; onChange: (rows: VlanRow[]) => void; problems: Record<number, string> }) {
  return (
    <div className="space-y-2">
      {rows.map((r, i) => (
        <div key={r.key} className="flex flex-wrap items-end gap-2">
          <Field label={`VLAN id ${i + 1}`} required className="w-32">
            <Input aria-label={`VLAN ${i + 1} id`} type="number" min={1} max={4094} value={r.vlan}
              onChange={(e) => onChange(rows.map((x) => (x.key === r.key ? { ...x, vlan: e.target.value } : x)))} />
          </Field>
          <Field label="Name" className="min-w-48 flex-1">
            <Input aria-label={`VLAN ${i + 1} name`} value={r.name} placeholder="Extension 1"
              onChange={(e) => onChange(rows.map((x) => (x.key === r.key ? { ...x, name: e.target.value } : x)))} />
          </Field>
          <Button type="button" variant="ghost" aria-label={`Remove VLAN ${i + 1}`} disabled={rows.length === 1}
            onClick={() => onChange(rows.filter((x) => x.key !== r.key))}><Trash2 size={14} /></Button>
          {problems[r.key] && <p className="w-full text-2xs text-destructive" role="status">{problems[r.key]}</p>}
        </div>
      ))}
      <Button type="button" variant="secondary" disabled={rows.length >= 32}
        onClick={() => onChange([...rows, newVlanRow()])}><Plus size={14} /> Add another VLAN</Button>
    </div>
  );
}

/** Per-VLAN addressing, and (in "separate" mode) per-VLAN DHCP and DNS. */
export function VlanAddressingTable({
  rows, onChange, problems, separate,
}: {
  rows: VlanRow[];
  onChange: (rows: VlanRow[]) => void;
  problems: Record<number, string>;
  separate: boolean;
}) {
  const patch = (key: number, p: Partial<VlanRow>) =>
    onChange(rows.map((x) => (x.key === key ? { ...x, ...p } : x)));
  return (
    <Table>
      <THead>
        <TR>
          {separate && <TH />}
          <TH>Network</TH><TH>VLAN</TH><TH>Subnet</TH><TH>Gateway</TH><TH>DHCP pool</TH>
        </TR>
      </THead>
      <TBody>
        {rows.map((r, i) => (
          <>
            <TR key={r.key}>
              {separate && (
                <TD className="w-8">
                  <Button type="button" variant="ghost" size="sm" aria-label={`More settings for VLAN ${i + 1}`}
                    onClick={() => patch(r.key, { open: !r.open })}>
                    {r.open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                  </Button>
                </TD>
              )}
              <TD>
                <Input aria-label={`VLAN ${i + 1} name`} value={r.name}
                  onChange={(e) => patch(r.key, { name: e.target.value })} />
              </TD>
              <TD className="w-20"><Badge tone="info">{r.vlan || "—"}</Badge></TD>
              <TD>
                <Input aria-label={`VLAN ${i + 1} subnet`} className="font-mono" value={r.subnet}
                  onChange={(e) => patch(r.key, { subnet: e.target.value, touched: true, ...addressingFor(e.target.value) })} />
              </TD>
              <TD>
                <Input aria-label={`VLAN ${i + 1} gateway`} className="font-mono" value={r.gateway}
                  onChange={(e) => patch(r.key, { gateway: e.target.value, touched: true })} />
              </TD>
              <TD>
                <div className="flex items-center gap-1">
                  <Input aria-label={`VLAN ${i + 1} pool start`} className="font-mono" value={r.poolStart}
                    onChange={(e) => patch(r.key, { poolStart: e.target.value, touched: true })} />
                  <span aria-hidden>–</span>
                  <Input aria-label={`VLAN ${i + 1} pool end`} className="font-mono" value={r.poolEnd}
                    onChange={(e) => patch(r.key, { poolEnd: e.target.value, touched: true })} />
                </div>
                {problems[r.key] && <p className="mt-1 text-2xs text-destructive" role="status">{problems[r.key]}</p>}
              </TD>
            </TR>
            {separate && r.open && (
              <TR key={`${r.key}-adv`}>
                <TD colSpan={6}>
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                    <Field label="DNS">
                      <Select aria-label={`VLAN ${i + 1} DNS mode`} value={r.dnsMode}
                        onChange={(e) => patch(r.key, { dnsMode: e.target.value as "appliance" | "custom" })}>
                        <option value="appliance">This appliance</option>
                        <option value="custom">Named servers</option>
                      </Select>
                    </Field>
                    {r.dnsMode === "custom" && (
                      <Field label="DNS servers" hint="Comma separated.">
                        <Input aria-label={`VLAN ${i + 1} DNS servers`} value={r.dnsServers}
                          onChange={(e) => patch(r.key, { dnsServers: e.target.value })} />
                      </Field>
                    )}
                    <Field label="Search domain">
                      <Input aria-label={`VLAN ${i + 1} domain`} value={r.domainName}
                        onChange={(e) => patch(r.key, { domainName: e.target.value })} />
                    </Field>
                    <Field label="Lease (s)" hint="Default / min / max.">
                      <div className="flex items-center gap-1">
                        <Input aria-label={`VLAN ${i + 1} lease default`} value={r.leaseDefault}
                          onChange={(e) => patch(r.key, { leaseDefault: e.target.value })} />
                        <Input aria-label={`VLAN ${i + 1} lease min`} value={r.leaseMin}
                          onChange={(e) => patch(r.key, { leaseMin: e.target.value })} />
                        <Input aria-label={`VLAN ${i + 1} lease max`} value={r.leaseMax}
                          onChange={(e) => patch(r.key, { leaseMax: e.target.value })} />
                      </div>
                    </Field>
                  </div>
                </TD>
              </TR>
            )}
          </>
        ))}
      </TBody>
    </Table>
  );
}
