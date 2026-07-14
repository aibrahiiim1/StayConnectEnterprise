"use client";

import { useEffect, useState } from "react";
import {
  api, ListResp, Whoami,
  DhcpLease, Reservation, GuestNetwork,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { errMsg, formatRelative } from "@/lib/utils";

function leaseState(s?: number | string): string {
  if (s === 0 || s === "0" || s == null) return "active";
  if (s === 1 || s === "1") return "declined";
  if (s === 2 || s === "2") return "expired";
  return String(s);
}

function leaseExpiry(l: DhcpLease): string {
  if (l.cltt && l["valid-lft"]) {
    return formatRelative(new Date((l.cltt + l["valid-lft"]) * 1000).toISOString());
  }
  return "—";
}

export default function DhcpPage() {
  const [tab, setTab] = useState<"leases" | "reservations">("leases");
  const [roles, setRoles] = useState<string[]>([]);
  const [leases, setLeases] = useState<DhcpLease[] | null>(null);
  const [reservations, setReservations] = useState<Reservation[] | null>(null);
  const [networks, setNetworks] = useState<GuestNetwork[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [showNew, setShowNew] = useState(false);
  const [newRes, setNewRes] = useState({ guest_network_id: "", mac: "", reserved_ip: "", hostname: "", enabled: true });
  const [editRes, setEditRes] = useState<Reservation | null>(null);

  const writable = canWrite("network", roles);
  const netName = (gid: string) => networks.find((n) => n.id === gid)?.name ?? gid;

  async function loadLeases() {
    try { setLeases((await api.get<{ leases: DhcpLease[] }>("/network/dhcp/leases")).leases ?? []); }
    catch (e) { setErr(errMsg(e)); }
  }
  async function loadReservations() {
    try { setReservations((await api.get<ListResp<Reservation>>("/network/dhcp/reservations")).data ?? []); }
    catch (e) { setErr(errMsg(e)); }
  }

  useEffect(() => {
    loadLeases();
    loadReservations();
    api.get<ListResp<GuestNetwork>>("/network/guest-networks").then((r) => setNetworks(r.data ?? [])).catch(() => {});
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);

  async function onCreate() {
    if (!newRes.guest_network_id || !newRes.mac.trim() || !newRes.reserved_ip.trim()) {
      setErr("Guest network, MAC and reserved IP are required."); return;
    }
    setBusy(true); setErr(null);
    try {
      await api.post("/network/dhcp/reservations", {
        guest_network_id: newRes.guest_network_id, mac: newRes.mac.trim(), reserved_ip: newRes.reserved_ip.trim(),
        hostname: newRes.hostname.trim() || undefined, enabled: newRes.enabled,
      });
      setShowNew(false);
      setNewRes({ guest_network_id: "", mac: "", reserved_ip: "", hostname: "", enabled: true });
      loadReservations();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onUpdate() {
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

  async function onDelete(id: string) {
    if (!confirm("Delete this reservation?")) return;
    try { await api.del(`/network/dhcp/reservations/${id}`); loadReservations(); }
    catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Networking</div>
          <h1 className="text-2xl font-semibold">DHCP &amp; leases</h1>
        </div>
        {tab === "reservations" && writable && (
          <Button onClick={() => setShowNew((v) => !v)}>
            {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New reservation</>}
          </Button>
        )}
      </div>

      <div className="flex gap-2 mb-4">
        <button
          onClick={() => setTab("leases")}
          className={"px-3 py-1.5 rounded-md text-sm border " + (tab === "leases" ? "bg-panel2 text-text border-border" : "text-muted border-transparent hover:text-text")}
        >Active leases</button>
        <button
          onClick={() => setTab("reservations")}
          className={"px-3 py-1.5 rounded-md text-sm border " + (tab === "reservations" ? "bg-panel2 text-text border-border" : "text-muted border-transparent hover:text-text")}
        >Reservations</button>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {tab === "reservations" && showNew && writable && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New reservation</CardTitle></CardHeader>
          <CardBody>
            <div className="grid grid-cols-1 sm:grid-cols-5 gap-3 items-end">
              <div>
                <Label>Guest network</Label>
                <select value={newRes.guest_network_id} onChange={(e) => setNewRes({ ...newRes, guest_network_id: e.target.value })}
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  <option value="">Select…</option>
                  {networks.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
                </select>
              </div>
              <div><Label>MAC</Label><Input value={newRes.mac} onChange={(e) => setNewRes({ ...newRes, mac: e.target.value })} placeholder="aa:bb:cc:dd:ee:ff" /></div>
              <div><Label>Reserved IP</Label><Input value={newRes.reserved_ip} onChange={(e) => setNewRes({ ...newRes, reserved_ip: e.target.value })} placeholder="10.20.0.50" /></div>
              <div><Label>Hostname</Label><Input value={newRes.hostname} onChange={(e) => setNewRes({ ...newRes, hostname: e.target.value })} placeholder="Optional" /></div>
              <div className="flex items-center gap-3">
                <label className="flex items-center gap-2 text-sm text-muted h-9"><input type="checkbox" checked={newRes.enabled} onChange={(e) => setNewRes({ ...newRes, enabled: e.target.checked })} /> On</label>
                <Button disabled={busy} onClick={onCreate}>{busy ? "Adding…" : "Add"}</Button>
              </div>
            </div>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {tab === "leases" ? (
            leases === null ? <EmptyState title="Loading…" /> : leases.length === 0 ? (
              <EmptyState title="No active leases" hint="Leases appear here once guests connect and Kea hands out addresses." />
            ) : (
              <Table>
                <THead><TR><TH>IP address</TH><TH>MAC</TH><TH>Hostname</TH><TH>Subnet</TH><TH>State</TH><TH>Expires</TH></TR></THead>
                <tbody>
                  {leases.map((l, i) => (
                    <TR key={i}>
                      <TD className="font-mono text-xs">{l["ip-address"]}</TD>
                      <TD className="font-mono text-xs">{l["hw-address"]}</TD>
                      <TD className="text-muted">{l.hostname || "—"}</TD>
                      <TD className="text-muted">{l["subnet-id"] ?? "—"}</TD>
                      <TD><Badge tone={leaseState(l.state) === "active" ? "ok" : "warn"}>{leaseState(l.state)}</Badge></TD>
                      <TD className="text-muted">{leaseExpiry(l)}</TD>
                    </TR>
                  ))}
                </tbody>
              </Table>
            )
          ) : (
            reservations === null ? <EmptyState title="Loading…" /> : reservations.length === 0 ? (
              <EmptyState title="No reservations" hint="Pin device MACs to fixed IPs across your guest networks." />
            ) : (
              <Table>
                <THead><TR><TH>Guest network</TH><TH>MAC</TH><TH>Reserved IP</TH><TH>Hostname</TH><TH>Enabled</TH><TH></TH></TR></THead>
                <tbody>
                  {reservations.map((r) => (
                    <TR key={r.id}>
                      <TD>{netName(r.guest_network_id)}</TD>
                      <TD className="font-mono text-xs">{r.mac}</TD>
                      <TD className="font-mono text-xs">{r.reserved_ip}</TD>
                      <TD className="text-muted">{r.hostname || "—"}</TD>
                      <TD>{r.enabled ? <Badge tone="ok">on</Badge> : <Badge tone="default">off</Badge>}</TD>
                      <TD className="text-right space-x-2">
                        {writable && <Button size="sm" variant="ghost" onClick={() => setEditRes(r)}>Edit</Button>}
                        {writable && <Button size="sm" variant="ghost" onClick={() => onDelete(r.id)}>Delete</Button>}
                      </TD>
                    </TR>
                  ))}
                </tbody>
              </Table>
            )
          )}
        </CardBody>
      </Card>

      {editRes && (
        <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-6 z-50" onClick={() => setEditRes(null)}>
          <div className="bg-panel border border-border rounded-lg shadow-panel max-w-lg w-full" onClick={(e) => e.stopPropagation()}>
            <div className="px-5 py-4 border-b border-border flex items-center justify-between">
              <h2 className="text-base font-semibold">Edit reservation</h2>
              <Button size="sm" variant="ghost" onClick={() => setEditRes(null)}><X size={14} /></Button>
            </div>
            <div className="px-5 py-4 space-y-3">
              <div><Label>Guest network</Label><Input value={netName(editRes.guest_network_id)} disabled /></div>
              <div><Label>MAC</Label><Input value={editRes.mac} disabled className="font-mono" /></div>
              <div><Label>Reserved IP</Label><Input value={editRes.reserved_ip} onChange={(e) => setEditRes({ ...editRes, reserved_ip: e.target.value })} /></div>
              <div><Label>Hostname</Label><Input value={editRes.hostname ?? ""} onChange={(e) => setEditRes({ ...editRes, hostname: e.target.value })} /></div>
              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" checked={editRes.enabled} onChange={(e) => setEditRes({ ...editRes, enabled: e.target.checked })} /> Enabled</label>
              <div className="flex justify-end"><Button disabled={busy} onClick={onUpdate}>{busy ? "Saving…" : "Save"}</Button></div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
