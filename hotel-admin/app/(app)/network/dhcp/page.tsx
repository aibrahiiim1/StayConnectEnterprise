"use client";

import { useEffect, useState } from "react";
import {
  api, ListResp,
  DhcpLease, Reservation, GuestNetwork,
} from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageShell, PageHeader, Toolbar } from "@/components/ui/page";
import { SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { Pencil, Pin, Plus, Trash2, Wifi } from "lucide-react";
import { errMsg, formatRelative } from "@/lib/utils";
import {
  AddReservationDialog, EditReservationDialog, RemoveReservationDialog, useNetworkAccess,
} from "@/components/network/shared";

function leaseState(s?: number | string): string {
  if (s === 0 || s === "0" || s == null) return "active";
  if (s === 1 || s === "1") return "declined";
  if (s === 2 || s === "2") return "expired";
  return String(s);
}

const LEASE_STATE: Record<string, { label: string; tone: "ok" | "warn" | "default" }> = {
  active: { label: "Active", tone: "ok" },
  declined: { label: "Declined", tone: "warn" },
  expired: { label: "Expired", tone: "warn" },
};

function leaseExpiry(l: DhcpLease): string {
  if (l.cltt && l["valid-lft"]) {
    return formatRelative(new Date((l.cltt + l["valid-lft"]) * 1000).toISOString());
  }
  return "—";
}

function matches(q: string, ...fields: (string | number | undefined | null)[]): boolean {
  if (!q) return true;
  const needle = q.trim().toLowerCase();
  return fields.some((f) => f != null && String(f).toLowerCase().includes(needle));
}

export default function DhcpPage() {
  const [tab, setTab] = useState<"leases" | "reservations">("leases");
  const { known, writable } = useNetworkAccess();
  const [leases, setLeases] = useState<DhcpLease[] | null>(null);
  const [reservations, setReservations] = useState<Reservation[] | null>(null);
  const [networks, setNetworks] = useState<GuestNetwork[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const [adding, setAdding] = useState(false);
  const [editRes, setEditRes] = useState<Reservation | null>(null);
  const [removing, setRemoving] = useState<Reservation | null>(null);
  const toast = useToast();

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
  }, []);

  const shownLeases = (leases ?? []).filter((l) => matches(q, l["ip-address"], l["hw-address"], l.hostname));
  const shownRes = (reservations ?? []).filter((r) => matches(q, r.reserved_ip, r.mac, r.hostname, netName(r.guest_network_id)));

  const noMatch = (what: string) => (
    <EmptyState
      title={`No ${what} match “${q}”`}
      hint="Search looks at the IP address, MAC address and hostname."
      action={<Button variant="secondary" size="sm" onClick={() => setQ("")}>Clear search</Button>}
    />
  );

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<Wifi />}
        eyebrow="Networking"
        title="DHCP & leases"
        description="Which guest devices hold an address right now, and which devices always get the same one."
        actions={writable && (
          <Button onClick={() => { setTab("reservations"); setAdding(true); }}>
            <Plus /> New reservation
          </Button>
        )}
      />

      {known && !writable && <ReadOnlyNotice>Your role can view leases and reservations but not change them.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      <Tabs value={tab} onValueChange={(v) => setTab(v as "leases" | "reservations")}>
        <Toolbar className="items-center">
          <TabsList className="border-b-0">
            <TabsTrigger value="leases">
              Active leases {leases !== null && <Badge tone="neutral">{leases.length}</Badge>}
            </TabsTrigger>
            <TabsTrigger value="reservations">
              Reservations {reservations !== null && <Badge tone="neutral">{reservations.length}</Badge>}
            </TabsTrigger>
          </TabsList>
          <SearchInput value={q} onChange={setQ} placeholder="Search IP, MAC or hostname" />
        </Toolbar>

        <TabsContent value="leases" className="mt-4">
          <Card>
            {leases === null ? (
              <SkeletonRows rows={5} cols={5} />
            ) : leases.length === 0 ? (
              <EmptyState icon={<Wifi />} title="No active leases" hint="Leases appear here once guests connect and are given an address." />
            ) : shownLeases.length === 0 ? (
              noMatch("leases")
            ) : (
              <Table>
                <THead>
                  <TR>
                    <TH>IP address</TH>
                    <TH className="hidden sm:table-cell">MAC address</TH>
                    <TH className="hidden md:table-cell">Hostname</TH>
                    <TH className="hidden lg:table-cell">Subnet ID</TH>
                    <TH>State</TH>
                    <TH className="hidden sm:table-cell">Expires</TH>
                  </TR>
                </THead>
                <TBody>
                  {shownLeases.map((l, i) => {
                    const st = leaseState(l.state);
                    const s = LEASE_STATE[st] ?? { label: st, tone: "default" as const };
                    return (
                      <TR key={i}>
                        <TD className="font-mono text-xs">
                          {l["ip-address"]}
                          <div className="font-mono text-caption text-muted-foreground sm:hidden">{l["hw-address"]}</div>
                        </TD>
                        <TD className="hidden font-mono text-xs sm:table-cell">{l["hw-address"]}</TD>
                        <TD className="hidden text-muted-foreground md:table-cell">{l.hostname || "—"}</TD>
                        <TD className="hidden tabular text-muted-foreground lg:table-cell">{l["subnet-id"] ?? "—"}</TD>
                        <TD><Badge tone={s.tone}>{s.label}</Badge></TD>
                        <TD className="hidden text-muted-foreground sm:table-cell">{leaseExpiry(l)}</TD>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            )}
          </Card>
        </TabsContent>

        <TabsContent value="reservations" className="mt-4">
          <Card>
            {reservations === null ? (
              <SkeletonRows rows={4} cols={5} />
            ) : reservations.length === 0 ? (
              <EmptyState
                icon={<Pin />}
                title="No reservations"
                hint="Pin a device to a fixed address on one of your guest networks — a printer, a TV or a door lock."
                action={writable ? <Button size="sm" onClick={() => setAdding(true)}><Plus /> New reservation</Button> : undefined}
              />
            ) : shownRes.length === 0 ? (
              noMatch("reservations")
            ) : (
              <Table>
                <THead>
                  <TR>
                    <TH>Guest network</TH>
                    <TH className="hidden sm:table-cell">MAC address</TH>
                    <TH>Reserved IP</TH>
                    <TH className="hidden md:table-cell">Hostname</TH>
                    <TH className="hidden sm:table-cell">Status</TH>
                    {writable && <TH><span className="sr-only">Actions</span></TH>}
                  </TR>
                </THead>
                <TBody>
                  {shownRes.map((r) => (
                    <TR key={r.id}>
                      <TD>{netName(r.guest_network_id)}</TD>
                      <TD className="hidden font-mono text-xs sm:table-cell">{r.mac}</TD>
                      <TD className="font-mono text-xs">{r.reserved_ip}</TD>
                      <TD className="hidden text-muted-foreground md:table-cell">{r.hostname || "—"}</TD>
                      <TD className="hidden sm:table-cell">{r.enabled ? <Badge tone="ok">Enabled</Badge> : <Badge tone="default">Disabled</Badge>}</TD>
                      {writable && (
                        <TD className="whitespace-nowrap text-end">
                          <Button size="icon-sm" variant="ghost" aria-label={`Edit reservation ${r.reserved_ip}`} onClick={() => setEditRes(r)}><Pencil /></Button>
                          <Button size="icon-sm" variant="ghost" aria-label={`Remove reservation ${r.reserved_ip}`} onClick={() => setRemoving(r)}><Trash2 /></Button>
                        </TD>
                      )}
                    </TR>
                  ))}
                </TBody>
              </Table>
            )}
          </Card>
        </TabsContent>
      </Tabs>

      {writable && (
        <>
          <AddReservationDialog
            open={adding}
            onOpenChange={setAdding}
            networks={networks}
            onSaved={() => { toast.success("Reservation added"); loadReservations(); }}
          />
          <EditReservationDialog
            reservation={editRes}
            networkName={editRes ? netName(editRes.guest_network_id) : undefined}
            onClose={() => setEditRes(null)}
            onSaved={() => { toast.success("Reservation saved"); loadReservations(); }}
          />
          <RemoveReservationDialog
            reservation={removing}
            onClose={() => setRemoving(null)}
            onRemoved={() => { toast.success("Reservation removed"); loadReservations(); }}
          />
        </>
      )}
    </PageShell>
  );
}
