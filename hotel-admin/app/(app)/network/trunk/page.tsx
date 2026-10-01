"use client";

// SEVERAL VLANS ON ONE TRUNK, IN ONE STEP.
//
// A trunk that carries the client VLANs of two hotel extensions (or a staff VLAN beside the guest one) is several
// client networks: each VLAN is its own network with its OWN subnet and its OWN DHCP scope, so they stay isolated
// at layer 2 and layer 3. What used to make this tedious was the wizard -- one network at a time, an apply after
// each. Here the operator lays out the trunk once; every VLAN gets a suggested, non-overlapping /24 it can change;
// they are created together in one transaction (all or none, overlap refused by the server too) and then applied
// once through the usual validate -> apply -> confirm, with automatic rollback.

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ApiError, GuestNetwork, Interface } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Field, Input, Select } from "@/components/ui/input";
import { Button, buttonVariants } from "@/components/ui/button";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { HelpSection } from "@/components/help";
import { useNetworkAccess } from "@/components/network/shared";
import { addressingFor, suggestSubnet } from "@/lib/network-trunk";
import { Network, Plus, Trash2, ArrowLeft } from "lucide-react";

const SELECTABLE = new Set(["guest_trunk", "guest_access", "unused"]);

type Row = { key: number; name: string; vlan: string; subnet: string; gateway: string; poolStart: string; poolEnd: string; touched: boolean };

let nextKey = 1;
const emptyRow = (): Row => ({ key: nextKey++, name: "", vlan: "", subnet: "", gateway: "", poolStart: "", poolEnd: "", touched: false });

export default function TrunkVLANsPage() {
  const { known, writable } = useNetworkAccess();
  const [interfaces, setInterfaces] = useState<Interface[] | null>(null);
  const [existing, setExisting] = useState<GuestNetwork[]>([]);
  const [parent, setParent] = useState("");
  const [rows, setRows] = useState<Row[]>([emptyRow(), emptyRow()]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [created, setCreated] = useState<{ name: string; bridge_name: string }[] | null>(null);

  useEffect(() => {
    api.get<{ interfaces: Interface[] }>("/network/interfaces").then((r) => setInterfaces(r.interfaces ?? [])).catch((e) => setErr(e));
    api.get<{ data: GuestNetwork[] }>("/network/guest-networks").then((r) => setExisting(r.data ?? [])).catch(() => setExisting([]));
  }, []);

  const usedSubnets = useMemo(() => new Set(existing.filter((n) => n.enabled).map((n) => n.subnet_cidr)), [existing]);
  const usedVLANs = useMemo(
    () => new Set(existing.filter((n) => n.enabled && n.parent_interface === parent && n.vlan_id).map((n) => n.vlan_id!)),
    [existing, parent]);

  function update(key: number, patch: Partial<Row>) {
    setRows((rs) => {
      const next = rs.map((r) => (r.key === key ? { ...r, ...patch } : r));
      // Suggest addressing for a row the operator has not edited, the moment it has a VLAN.
      return next.map((r) => {
        if (r.key !== key || r.touched || !("vlan" in patch)) return r;
        const v = Number(r.vlan);
        if (!Number.isInteger(v) || v < 1 || v > 4094) return r;
        const used = new Set([...usedSubnets, ...next.filter((o) => o.key !== key && o.subnet).map((o) => o.subnet)]);
        const subnet = suggestSubnet(v, used);
        return { ...r, subnet, ...addressingFor(subnet), name: r.name || `VLAN ${v}` };
      });
    });
  }

  const problems = useMemo(() => {
    const out: Record<number, string> = {};
    const seenVLAN = new Map<number, number>();
    const seenSubnet = new Map<string, number>();
    for (const r of rows) {
      const v = Number(r.vlan);
      if (!r.name.trim()) out[r.key] = "Name it.";
      else if (!Number.isInteger(v) || v < 1 || v > 4094) out[r.key] = "VLAN 1–4094.";
      else if (usedVLANs.has(v)) out[r.key] = `VLAN ${v} is already on ${parent}.`;
      else if (seenVLAN.has(v)) out[r.key] = `VLAN ${v} is listed twice.`;
      else if (!r.subnet || !r.gateway || !r.poolStart || !r.poolEnd) out[r.key] = "Subnet, gateway and pool are needed.";
      else if (usedSubnets.has(r.subnet)) out[r.key] = "That subnet is already used by another network.";
      else if (seenSubnet.has(r.subnet)) out[r.key] = "Each VLAN needs its own subnet.";
      seenVLAN.set(v, r.key);
      seenSubnet.set(r.subnet, r.key);
    }
    return out;
  }, [rows, usedVLANs, usedSubnets, parent]);

  const ready = !!parent && rows.length > 0 && Object.keys(problems).length === 0;

  async function create() {
    if (!ready) return;
    setBusy(true); setErr(null);
    try {
      const r = await api.post<{ networks: { name: string; bridge_name: string }[] }>("/network/guest-networks/batch", {
        networks: rows.map((row) => ({
          name: row.name.trim(), network_type: "vlan", parent_interface: parent, vlan_id: Number(row.vlan),
          subnet_cidr: row.subnet.trim(), gateway_ip: row.gateway.trim(),
          pools: [{ start_ip: row.poolStart.trim(), end_ip: row.poolEnd.trim() }],
        })),
      });
      setCreated(r.networks);
    } catch (e) {
      setErr(e instanceof ApiError ? e : new Error("the networks could not be created"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<Network />}
        eyebrow="Networking"
        title="Add VLANs on a trunk"
        description="Several client networks on one tagged port, each with its own subnet and DHCP scope."
        help={
          <HelpSection title="Isolation">
            <p>
              Each VLAN becomes its own client network with its own subnet, gateway and DHCP pool, so clients on one VLAN
              never share an address range or a broadcast domain with another. Route each one to its PMS on PMS routing.
            </p>
          </HelpSection>
        }
        actions={<Link href="/network" className={buttonVariants({ variant: "secondary" })}><ArrowLeft /> Client networks</Link>}
      />
      {known && !writable && <ReadOnlyNotice>Your role can view client networks but not create them.</ReadOnlyNotice>}
      <ErrorBanner err={err} />

      {created ? (
        <Callout tone="success" title={`${created.length} client networks created — not applied yet`}>
          {created.map((c) => c.name).join(", ")}. <Link href="/network" className="font-medium underline">Go to Client networks</Link> to
          validate and apply them together, then confirm. Afterwards, route each one to its PMS on{" "}
          <Link href="/pms-routing" className="font-medium underline">PMS routing</Link>.
        </Callout>
      ) : (
        <Card>
          <CardHeader>
            <div className="space-y-1">
              <CardTitle>Trunk port and VLANs</CardTitle>
              <CardDescription>Suggested subnets never overlap existing networks; change them if your plan differs.</CardDescription>
            </div>
          </CardHeader>
          <CardBody className="space-y-4">
            <Field label="Trunk port" required className="max-w-xs">
              <Select aria-label="Trunk port" value={parent} onChange={(e) => setParent(e.target.value)}>
                <option value="">Choose…</option>
                {(interfaces ?? []).map((i) => (
                  <option key={i.name} value={i.name} disabled={!SELECTABLE.has(i.role ?? "")}>
                    {i.name}{i.role ? ` · ${i.role.replace(/_/g, " ")}` : ""}
                  </option>
                ))}
              </Select>
            </Field>
            <Table>
              <THead>
                <TR><TH>Name</TH><TH>VLAN</TH><TH>Subnet</TH><TH>Gateway</TH><TH>DHCP pool</TH><TH /></TR>
              </THead>
              <TBody>
                {rows.map((r, i) => (
                  <TR key={r.key}>
                    <TD><Input aria-label={`VLAN ${i + 1} name`} value={r.name} onChange={(e) => update(r.key, { name: e.target.value })} placeholder="Extension 1" /></TD>
                    <TD className="w-28"><Input aria-label={`VLAN ${i + 1} id`} type="number" min={1} max={4094} value={r.vlan} onChange={(e) => update(r.key, { vlan: e.target.value })} /></TD>
                    <TD><Input aria-label={`VLAN ${i + 1} subnet`} className="font-mono" value={r.subnet}
                      onChange={(e) => update(r.key, { subnet: e.target.value, touched: true, ...addressingFor(e.target.value) })} /></TD>
                    <TD><Input aria-label={`VLAN ${i + 1} gateway`} className="font-mono" value={r.gateway} onChange={(e) => update(r.key, { gateway: e.target.value, touched: true })} /></TD>
                    <TD>
                      <div className="flex items-center gap-1">
                        <Input aria-label={`VLAN ${i + 1} pool start`} className="font-mono" value={r.poolStart} onChange={(e) => update(r.key, { poolStart: e.target.value, touched: true })} />
                        <span aria-hidden>–</span>
                        <Input aria-label={`VLAN ${i + 1} pool end`} className="font-mono" value={r.poolEnd} onChange={(e) => update(r.key, { poolEnd: e.target.value, touched: true })} />
                      </div>
                      {problems[r.key] && <p className="mt-1 text-2xs text-destructive" role="status">{problems[r.key]}</p>}
                    </TD>
                    <TD className="text-right">
                      <Button variant="ghost" size="sm" aria-label={`Remove VLAN ${i + 1}`} disabled={rows.length === 1}
                        onClick={() => setRows((rs) => rs.filter((x) => x.key !== r.key))}><Trash2 /></Button>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" onClick={() => setRows((rs) => [...rs, emptyRow()])} disabled={rows.length >= 32}><Plus /> Add a VLAN</Button>
              {writable && (
                <Button onClick={() => void create()} disabled={!ready || busy}>
                  {busy ? "Creating…" : `Create ${rows.length} network${rows.length === 1 ? "" : "s"}`}
                </Button>
              )}
            </div>
          </CardBody>
        </Card>
      )}
    </PageShell>
  );
}
