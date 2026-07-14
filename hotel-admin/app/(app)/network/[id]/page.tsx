"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import {
  api, ListResp, Whoami, Pool,
  GuestNetwork, GuestNetworkStatus, Reservation, GuestNetworkInput,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ArrowLeft, Plus, X } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";

export default function EditGuestNetworkPage() {
  const { id } = useParams<{ id: string }>();
  const [roles, setRoles] = useState<string[]>([]);
  const [net, setNet] = useState<GuestNetwork | null>(null);
  const [status, setStatus] = useState<GuestNetworkStatus | null>(null);
  const [reservations, setReservations] = useState<Reservation[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // editable form state
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [ssidLabel, setSsidLabel] = useState("");
  const [subnetCidr, setSubnetCidr] = useState("");
  const [gatewayIp, setGatewayIp] = useState("");
  const [pools, setPools] = useState<Pool[]>([]);
  const [dnsMode, setDnsMode] = useState("appliance");
  const [dnsServers, setDnsServers] = useState("");
  const [domainName, setDomainName] = useState("");
  const [leaseDefault, setLeaseDefault] = useState("");
  const [leaseMin, setLeaseMin] = useState("");
  const [leaseMax, setLeaseMax] = useState("");
  const [captivePortal, setCaptivePortal] = useState(true);
  const [internetAccess, setInternetAccess] = useState(true);
  const [nat, setNat] = useState(true);
  const [clientIsolation, setClientIsolation] = useState(false);

  // reservation form
  const [newRes, setNewRes] = useState({ mac: "", reserved_ip: "", hostname: "", enabled: true });
  const [editRes, setEditRes] = useState<Reservation | null>(null);

  const writable = canWrite("network", roles);

  function hydrate(g: GuestNetwork) {
    setNet(g);
    setName(g.name);
    setDescription(g.description ?? "");
    setSsidLabel(g.ssid_label ?? "");
    setSubnetCidr(g.subnet_cidr);
    setGatewayIp(g.gateway_ip);
    setPools(g.pools && g.pools.length ? g.pools : [{ start_ip: "", end_ip: "" }]);
    setDnsMode(g.dns_mode);
    setDnsServers((g.dns_servers ?? []).join(", "));
    setDomainName(g.domain_name);
    setLeaseDefault(String(g.lease_default_seconds));
    setLeaseMin(String(g.lease_min_seconds));
    setLeaseMax(String(g.lease_max_seconds));
    setCaptivePortal(g.captive_portal_enabled);
    setInternetAccess(g.internet_access_enabled);
    setNat(g.nat_enabled);
    setClientIsolation(g.client_isolation_enabled);
  }

  async function loadReservations() {
    try {
      const r = await api.get<ListResp<Reservation>>(`/network/dhcp/reservations?guest_network_id=${id}`);
      setReservations(r.data ?? []);
    } catch (e) { setErr(errMsg(e)); }
  }

  async function loadStatus() {
    try { setStatus(await api.get<GuestNetworkStatus>(`/network/guest-networks/${id}/status`)); }
    catch { /* status optional */ }
  }

  useEffect(() => {
    if (!id) return;
    api.get<GuestNetwork>(`/network/guest-networks/${id}`).then(hydrate).catch((e) => setErr(errMsg(e)));
    loadStatus();
    loadReservations();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, [id]);

  async function onSave() {
    setBusy(true); setErr(null); setMsg(null);
    const body: GuestNetworkInput = {
      name: name.trim(),
      description: description.trim() || undefined,
      ssid_label: ssidLabel.trim() || undefined,
      gateway_ip: gatewayIp.trim(),
      subnet_cidr: subnetCidr.trim(),
      dhcp_mode: net?.dhcp_mode ?? "local",
      dns_mode: dnsMode,
      dns_servers: dnsMode === "custom" ? dnsServers.split(",").map((s) => s.trim()).filter(Boolean) : undefined,
      domain_name: domainName.trim() || "guest.local",
      lease_default_seconds: Number(leaseDefault) || 3600,
      lease_min_seconds: Number(leaseMin) || 900,
      lease_max_seconds: Number(leaseMax) || 7200,
      captive_portal_enabled: captivePortal,
      internet_access_enabled: internetAccess,
      nat_enabled: nat,
      client_isolation_enabled: clientIsolation,
      pools: pools.filter((p) => p.start_ip && p.end_ip),
    };
    try {
      await api.put(`/network/guest-networks/${id}`, body);
      setMsg("Saved. Apply changes from the guest networks page to activate.");
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onCreateReservation() {
    if (!newRes.mac.trim() || !newRes.reserved_ip.trim()) { setErr("MAC and reserved IP are required."); return; }
    setBusy(true); setErr(null);
    try {
      await api.post("/network/dhcp/reservations", {
        guest_network_id: id, mac: newRes.mac.trim(), reserved_ip: newRes.reserved_ip.trim(),
        hostname: newRes.hostname.trim() || undefined, enabled: newRes.enabled,
      });
      setNewRes({ mac: "", reserved_ip: "", hostname: "", enabled: true });
      loadReservations();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onUpdateReservation() {
    if (!editRes) return;
    setBusy(true); setErr(null);
    try {
      await api.put(`/network/dhcp/reservations/${editRes.id}`, {
        reserved_ip: editRes.reserved_ip, hostname: editRes.hostname ?? "", enabled: editRes.enabled,
      });
      setEditRes(null);
      loadReservations();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onDeleteReservation(rid: string) {
    if (!confirm("Delete this reservation?")) return;
    try { await api.del(`/network/dhcp/reservations/${rid}`); loadReservations(); }
    catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <Link href="/network" className="text-sm text-muted hover:text-text inline-flex items-center gap-1 mb-4">
        <ArrowLeft size={14} /> Back to guest networks
      </Link>

      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Networking</div>
          <h1 className="text-2xl font-semibold">{net?.name || "Guest network"}</h1>
          {net && <div className="text-xs text-muted font-mono">{net.id}</div>}
        </div>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {msg && <div className="text-ok text-sm mb-4">{msg}</div>}

      {!net ? <EmptyState title="Loading…" /> : (
        <div className="space-y-6">
          {/* read-only topology + status */}
          <Card>
            <CardHeader><CardTitle>Status &amp; topology</CardTitle></CardHeader>
            <CardBody>
              <div className="grid grid-cols-2 sm:grid-cols-4 gap-4 text-sm">
                <Field label="Type" value={net.network_type === "vlan" ? `VLAN ${net.vlan_id ?? "?"}` : "untagged"} />
                <Field label="Parent interface" value={net.parent_interface} mono />
                <Field label="Bridge" value={net.bridge_name} mono />
                <Field label="Portal URL" value={net.portal_url ?? "—"} mono />
                <Field label="Enabled" value={status?.enabled ?? net.enabled ? "yes" : "no"} />
                <Field label="Active clients" value={String(status?.active_clients ?? "—")} />
              </div>
              <div className="text-xs text-muted mt-3">Type, VLAN, parent interface and bridge are immutable — delete and recreate to change topology.</div>
            </CardBody>
          </Card>

          {/* editable settings */}
          <Card>
            <CardHeader><CardTitle>Settings</CardTitle></CardHeader>
            <CardBody className="space-y-4">
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                <div><Label>Name</Label><Input value={name} onChange={(e) => setName(e.target.value)} disabled={!writable} /></div>
                <div><Label>SSID label</Label><Input value={ssidLabel} onChange={(e) => setSsidLabel(e.target.value)} disabled={!writable} /></div>
                <div><Label>Description</Label><Input value={description} onChange={(e) => setDescription(e.target.value)} disabled={!writable} /></div>
                <div><Label>Subnet CIDR</Label><Input value={subnetCidr} onChange={(e) => setSubnetCidr(e.target.value)} disabled={!writable} /></div>
                <div><Label>Gateway IP</Label><Input value={gatewayIp} onChange={(e) => setGatewayIp(e.target.value)} disabled={!writable} /></div>
                <div><Label>Domain name</Label><Input value={domainName} onChange={(e) => setDomainName(e.target.value)} disabled={!writable} /></div>
              </div>

              <div>
                <Label>DHCP pools</Label>
                <div className="space-y-2">
                  {pools.map((p, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <Input placeholder="start" value={p.start_ip} disabled={!writable}
                        onChange={(e) => setPools((ps) => ps.map((x, k) => k === i ? { ...x, start_ip: e.target.value } : x))} />
                      <span className="text-muted">–</span>
                      <Input placeholder="end" value={p.end_ip} disabled={!writable}
                        onChange={(e) => setPools((ps) => ps.map((x, k) => k === i ? { ...x, end_ip: e.target.value } : x))} />
                      {writable && (
                        <Button size="sm" variant="ghost" disabled={pools.length === 1}
                          onClick={() => setPools((ps) => ps.filter((_, k) => k !== i))}><X size={14} /></Button>
                      )}
                    </div>
                  ))}
                </div>
                {writable && (
                  <Button size="sm" variant="secondary" className="mt-2"
                    onClick={() => setPools((ps) => [...ps, { start_ip: "", end_ip: "" }])}>
                    <Plus size={14} /> Add pool
                  </Button>
                )}
              </div>

              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                <div>
                  <Label>DNS mode</Label>
                  <select value={dnsMode} onChange={(e) => setDnsMode(e.target.value)} disabled={!writable}
                    className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                    <option value="appliance">appliance</option>
                    <option value="custom">custom</option>
                  </select>
                </div>
                {dnsMode === "custom" && (
                  <div><Label>DNS servers (comma)</Label><Input value={dnsServers} onChange={(e) => setDnsServers(e.target.value)} disabled={!writable} /></div>
                )}
              </div>

              <div className="grid grid-cols-3 gap-3">
                <div><Label>Lease default (s)</Label><Input type="number" value={leaseDefault} onChange={(e) => setLeaseDefault(e.target.value)} disabled={!writable} /></div>
                <div><Label>Lease min (s)</Label><Input type="number" value={leaseMin} onChange={(e) => setLeaseMin(e.target.value)} disabled={!writable} /></div>
                <div><Label>Lease max (s)</Label><Input type="number" value={leaseMax} onChange={(e) => setLeaseMax(e.target.value)} disabled={!writable} /></div>
              </div>

              <div className="flex flex-wrap gap-4">
                <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" checked={captivePortal} disabled={!writable} onChange={(e) => setCaptivePortal(e.target.checked)} /> Captive portal</label>
                <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" checked={internetAccess} disabled={!writable} onChange={(e) => setInternetAccess(e.target.checked)} /> Internet access</label>
                <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" checked={nat} disabled={!writable} onChange={(e) => setNat(e.target.checked)} /> NAT</label>
                <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" checked={clientIsolation} disabled={!writable} onChange={(e) => setClientIsolation(e.target.checked)} /> Client isolation</label>
              </div>

              {writable && (
                <div className="flex justify-end">
                  <Button disabled={busy} onClick={onSave}>{busy ? "Saving…" : "Save"}</Button>
                </div>
              )}
            </CardBody>
          </Card>

          {/* reservations */}
          <Card>
            <CardHeader><CardTitle>DHCP reservations</CardTitle></CardHeader>
            <CardBody className="p-0">
              {reservations === null ? <EmptyState title="Loading…" /> : reservations.length === 0 ? (
                <EmptyState title="No reservations" hint="Pin a device MAC to a fixed IP inside this network." />
              ) : (
                <Table>
                  <THead><TR><TH>MAC</TH><TH>Reserved IP</TH><TH>Hostname</TH><TH>Enabled</TH><TH></TH></TR></THead>
                  <tbody>
                    {reservations.map((r) => (
                      <TR key={r.id}>
                        <TD className="font-mono text-xs">{r.mac}</TD>
                        <TD className="font-mono text-xs">{r.reserved_ip}</TD>
                        <TD className="text-muted">{r.hostname || "—"}</TD>
                        <TD>{r.enabled ? <Badge tone="ok">on</Badge> : <Badge tone="default">off</Badge>}</TD>
                        <TD className="text-right space-x-2">
                          {writable && <Button size="sm" variant="ghost" onClick={() => setEditRes(r)}>Edit</Button>}
                          {writable && <Button size="sm" variant="ghost" onClick={() => onDeleteReservation(r.id)}>Delete</Button>}
                        </TD>
                      </TR>
                    ))}
                  </tbody>
                </Table>
              )}
              {writable && (
                <div className="px-5 py-4 border-t border-border grid grid-cols-1 sm:grid-cols-5 gap-2 items-end">
                  <div><Label>MAC</Label><Input value={newRes.mac} onChange={(e) => setNewRes({ ...newRes, mac: e.target.value })} placeholder="aa:bb:cc:dd:ee:ff" /></div>
                  <div><Label>Reserved IP</Label><Input value={newRes.reserved_ip} onChange={(e) => setNewRes({ ...newRes, reserved_ip: e.target.value })} placeholder="10.20.0.50" /></div>
                  <div><Label>Hostname</Label><Input value={newRes.hostname} onChange={(e) => setNewRes({ ...newRes, hostname: e.target.value })} placeholder="Optional" /></div>
                  <label className="flex items-center gap-2 text-sm text-muted h-9"><input type="checkbox" checked={newRes.enabled} onChange={(e) => setNewRes({ ...newRes, enabled: e.target.checked })} /> Enabled</label>
                  <Button disabled={busy} onClick={onCreateReservation}><Plus size={14} /> Add</Button>
                </div>
              )}
            </CardBody>
          </Card>
        </div>
      )}

      {editRes && (
        <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-6 z-50" onClick={() => setEditRes(null)}>
          <div className="bg-panel border border-border rounded-lg shadow-panel max-w-lg w-full" onClick={(e) => e.stopPropagation()}>
            <div className="px-5 py-4 border-b border-border flex items-center justify-between">
              <h2 className="text-base font-semibold">Edit reservation</h2>
              <Button size="sm" variant="ghost" onClick={() => setEditRes(null)}><X size={14} /></Button>
            </div>
            <div className="px-5 py-4 space-y-3">
              <div><Label>MAC</Label><Input value={editRes.mac} disabled className="font-mono" /></div>
              <div><Label>Reserved IP</Label><Input value={editRes.reserved_ip} onChange={(e) => setEditRes({ ...editRes, reserved_ip: e.target.value })} /></div>
              <div><Label>Hostname</Label><Input value={editRes.hostname ?? ""} onChange={(e) => setEditRes({ ...editRes, hostname: e.target.value })} /></div>
              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" checked={editRes.enabled} onChange={(e) => setEditRes({ ...editRes, enabled: e.target.checked })} /> Enabled</label>
              <div className="flex justify-end"><Button disabled={busy} onClick={onUpdateReservation}>{busy ? "Saving…" : "Save"}</Button></div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div className="text-xs text-muted">{label}</div>
      <div className={mono ? "font-mono text-xs mt-0.5" : "text-sm mt-0.5"}>{value}</div>
    </div>
  );
}
