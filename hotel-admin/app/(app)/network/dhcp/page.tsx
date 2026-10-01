"use client";

import { useEffect, useMemo, useState } from "react";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
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
import { Pagination, SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { Pencil, Pin, Plus, Trash2, Wifi } from "lucide-react";
import { formatRelative } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";
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

type LeasesResp = PagedFields & { leases?: DhcpLease[] };
type ReservationsResp = PagedFields & { data?: Reservation[] };

export default function DhcpPage() {
  const [tab, setTab] = useState<"leases" | "reservations">("leases");
  const { known, writable } = useNetworkAccess();
  const [networks, setNetworks] = useState<GuestNetwork[]>([]);
  const [q, setQ] = useState("");
  const [adding, setAdding] = useState(false);
  const [editRes, setEditRes] = useState<Reservation | null>(null);
  const [removing, setRemoving] = useState<Reservation | null>(null);
  const toast = useToast();

  const netName = (gid: string) => networks.find((n) => n.id === gid)?.name ?? gid;

  // BOTH LISTS ARE PAGED AND SEARCHED IN EDGED. A property's pools hold thousands of leases; the screen used to
  // draw them all and filter in the browser. The search (an address, a MAC, a device name) goes in a header,
  // like every other list's, and a new search starts at the first page.
  const needle = q.trim();
  const headers = useMemo(
    () => (needle ? { "X-Dhcp-Search": encodeURIComponent(needle) } : undefined),
    [needle],
  );
  const leaseList = useServerPage<LeasesResp>({ path: "/network/dhcp/leases", headers, rowsOf: (r) => r.leases });
  const resList = useServerPage<ReservationsResp>({ path: "/network/dhcp/reservations", headers });
  const leases = leaseList.current && leaseList.resp ? leaseList.resp.leases ?? [] : leaseList.err && !leaseList.resp ? [] : null;
  const reservations = resList.current && resList.resp ? resList.resp.data ?? [] : resList.err && !resList.resp ? [] : null;
  const err = leaseList.err ?? resList.err;
  const loadReservations = () => void resList.reload();

  useEffect(() => {
    api.get<ListResp<GuestNetwork>>("/network/guest-networks").then((r) => setNetworks(r.data ?? [])).catch(() => {});
  }, []);

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
        description="Client devices holding an address now, and fixed reservations."
        help={
          <>
            <HelpSection title="Active leases">
              <p>
                Every client device that has been handed an address by the appliance&rsquo;s DHCP server, with the time its
                lease runs out. Leases appear once clients connect.
              </p>
            </HelpSection>
            <HelpSection title="Reservations">
              <HelpList
                items={[
                  "A reservation pins a device, by MAC address, to a fixed address on one client network.",
                  "Use it for devices that must stay reachable at one address — a printer, a TV or a door lock.",
                  "You can also manage reservations from each client network's own page.",
                ]}
              />
            </HelpSection>
            <HelpSection title="Search">
              <p>Search looks at the IP address, MAC address and hostname.</p>
            </HelpSection>
          </>
        }
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
              Active leases {leaseList.total !== null && <Badge tone="neutral">{leaseList.total}</Badge>}
            </TabsTrigger>
            <TabsTrigger value="reservations">
              Reservations {resList.total !== null && <Badge tone="neutral">{resList.total}</Badge>}
            </TabsTrigger>
          </TabsList>
          <SearchInput value={q} onChange={setQ} placeholder="Search IP, MAC or hostname" delay={300} />
        </Toolbar>

        <TabsContent value="leases" className="mt-4">
          <Card>
            {leases === null ? (
              <SkeletonRows rows={5} cols={5} />
            ) : leases.length === 0 && !needle ? (
              <EmptyState icon={<Wifi />} title="No active leases" hint="Leases appear here once clients connect and are given an address." />
            ) : leases.length === 0 ? (
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
                  {leases.map((l, i) => {
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
            {leases && leases.length > 0 && (leaseList.offset > 0 || leaseList.hasMore) && (
              <div className="border-t border-border px-4 py-3">
                <Pagination
                  offset={leaseList.offset}
                  limit={leaseList.pageSize}
                  shown={leases.length}
                  total={leaseList.total}
                  hasMore={leaseList.hasMore}
                  onChange={leaseList.setOffset}
                />
              </div>
            )}
          </Card>
        </TabsContent>

        <TabsContent value="reservations" className="mt-4">
          <Card>
            {reservations === null ? (
              <SkeletonRows rows={4} cols={5} />
            ) : reservations.length === 0 && !needle ? (
              <EmptyState
                icon={<Pin />}
                title="No reservations"
                hint="Pin a device to a fixed address on one of your client networks — a printer, a TV or a door lock."
                action={writable ? <Button size="sm" onClick={() => setAdding(true)}><Plus /> New reservation</Button> : undefined}
              />
            ) : reservations.length === 0 ? (
              noMatch("reservations")
            ) : (
              <Table>
                <THead>
                  <TR>
                    <TH>Client network</TH>
                    <TH className="hidden sm:table-cell">MAC address</TH>
                    <TH>Reserved IP</TH>
                    <TH className="hidden md:table-cell">Hostname</TH>
                    <TH className="hidden sm:table-cell">Status</TH>
                    {writable && <TH><span className="sr-only">Actions</span></TH>}
                  </TR>
                </THead>
                <TBody>
                  {reservations.map((r) => (
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
            {reservations && reservations.length > 0 && (resList.offset > 0 || resList.hasMore) && (
              <div className="border-t border-border px-4 py-3">
                <Pagination
                  offset={resList.offset}
                  limit={resList.pageSize}
                  shown={reservations.length}
                  total={resList.total}
                  hasMore={resList.hasMore}
                  onChange={resList.setOffset}
                />
              </div>
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
