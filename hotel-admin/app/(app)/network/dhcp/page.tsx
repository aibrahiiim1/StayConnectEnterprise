"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import {
  api, ListResp,
  DhcpLease, Reservation, GuestNetwork,
} from "@/lib/api";
import { Card, CardFooter } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Pagination, SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { FilterBar } from "@/components/ui/toolbar";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { LiveStatus, ReadOnlyNotice } from "@/components/ui/patterns";
import { RowActions } from "@/components/ui/row-actions";
import { useToast } from "@/components/ui/toast";
import { AlertTriangle, Pin, Plus, RefreshCw, Wifi } from "lucide-react";
import { errMsg } from "@/lib/utils";
import { usePoll } from "@/lib/use-poll";
import { cidrsOverlap } from "@/lib/network-trunk";
import { serviceStateOf } from "@/lib/network-state";
import { HelpList, HelpSection } from "@/components/help";
import {
  AddReservationDialog, EditReservationDialog, RemoveReservationDialog, ReservationTable, useNetworkAccess,
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

function span(ms: number): string {
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return "less than a minute";
  if (mins < 60) return `${mins}m`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return mins % 60 ? `${hrs}h ${mins % 60}m` : `${hrs}h`;
  return `${Math.floor(hrs / 24)}d`;
}

function LeaseExpiry({ lease, now }: { lease: DhcpLease; now: number }) {
  if (!lease.cltt || !lease["valid-lft"]) return <span>—</span>;
  const at = new Date((lease.cltt + lease["valid-lft"]) * 1000);
  const left = at.getTime() - now;
  return (
    <time dateTime={at.toISOString()} title={at.toLocaleString()}>
      {left > 0 ? `Expires in ${span(left)}` : `Expired ${span(-left)} ago`}
    </time>
  );
}

type LeasesResp = PagedFields & { leases?: DhcpLease[] };
type ReservationsResp = PagedFields & { data?: Reservation[] };
type Prefill = { guest_network_id: string; mac: string; reserved_ip: string; hostname: string };

const STAGED_NOTE = "It reaches the DHCP server when you apply the changes on Client networks.";

export default function DhcpPage() {
  const [tab, setTab] = useState<"leases" | "reservations">("leases");
  const { known, writable } = useNetworkAccess();
  const [networks, setNetworks] = useState<GuestNetwork[] | null>(null);
  const [netErr, setNetErr] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const [adding, setAdding] = useState(false);
  const [prefill, setPrefill] = useState<Prefill | null>(null);
  const [editRes, setEditRes] = useState<Reservation | null>(null);
  const [removing, setRemoving] = useState<Reservation | null>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const toast = useToast();

  const netName = (gid: string) =>
    networks?.find((n) => n.id === gid)?.name ?? (networks === null && !netErr ? "…" : "Unknown network");
  // A switched-off or archived network may overlap a live one: a lease belongs to the network serving it, never to an inactive match.
  const netOf = (ip: string) =>
    networks?.find((n) => n.subnet_cidr && serviceStateOf(n) === "serving" && cidrsOverlap(`${ip}/32`, n.subnet_cidr)) ?? null;

  const needle = q.trim();
  const headers = useMemo(
    () => (needle ? { "X-Dhcp-Search": encodeURIComponent(needle) } : undefined),
    [needle],
  );
  const leaseList = useServerPage<LeasesResp>({ path: "/network/dhcp/leases", headers, rowsOf: (r) => r.leases });
  const resList = useServerPage<ReservationsResp>({ path: "/network/dhcp/reservations", headers });
  const leases = leaseList.current && leaseList.resp ? leaseList.resp.leases ?? [] : null;
  const reservations = resList.current && resList.resp ? resList.resp.data ?? [] : null;
  const leaseFailed = !!leaseList.err && leases === null;
  const resFailed = !!resList.err && reservations === null;
  const loadReservations = () => void resList.reload();

  function loadNetworks() {
    setNetErr(null);
    api.get<ListResp<GuestNetwork>>("/network/guest-networks")
      .then((r) => setNetworks(r.data ?? []))
      .catch((e) => setNetErr(errMsg(e)));
  }

  useEffect(loadNetworks, []);

  useEffect(() => {
    if (leaseList.resp && !leaseList.err) setUpdatedAt(Date.now());
  }, [leaseList.resp, leaseList.err]);

  usePoll(() => { setNow(Date.now()); return leaseList.reload(); }, 30000, { enabled: tab === "leases" });

  function reserve(l: DhcpLease) {
    setPrefill({
      guest_network_id: netOf(l["ip-address"])?.id ?? "",
      mac: l["hw-address"],
      reserved_ip: l["ip-address"],
      hostname: l.hostname ?? "",
    });
    setAdding(true);
  }

  const noMatch = (what: string) => (
    <EmptyState
      size="sm"
      title={`No ${what} match “${q}”`}
      hint="Try part of the address, or clear the search."
      action={<Button variant="secondary" size="sm" onClick={() => setQ("")}>Clear search</Button>}
    />
  );

  const failed = (what: string, err: unknown, retry: () => void) => (
    <EmptyState
      size="sm"
      icon={<AlertTriangle />}
      title={`The ${what} could not be loaded`}
      hint={errMsg(err)}
      action={<Button variant="secondary" size="sm" onClick={retry}><RefreshCw /> Try again</Button>}
    />
  );

  const count = (total: number | null) =>
    total !== null && (
      <Badge tone="neutral" title={needle ? "Matching the search" : undefined}>
        {total.toLocaleString()}
        {needle && <span className="sr-only"> matching the search</span>}
      </Badge>
    );

  const searchBar = (
    <FilterBar inset search={<SearchInput value={q} onChange={setQ} placeholder="Search IP, MAC or hostname" delay={300} />} />
  );

  return (
    <PageShell width="wide">
      <PageHeader
        title="DHCP & leases"
        description="Client devices holding an address now, and fixed reservations."
        meta={tab === "leases" && leases !== null && (
          <LiveStatus
            updatedAt={updatedAt}
            refreshing={leaseList.loading}
            onRefresh={() => { setNow(Date.now()); void leaseList.reload(); }}
            intervalSeconds={30}
            error={!!leaseList.err}
          />
        )}
        help={
          <>
            <HelpSection title="Leases">
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
                  "A reservation is staged: it reaches the DHCP server when you next apply the client-network changes.",
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
          <Button onClick={() => { setTab("reservations"); setPrefill(null); setAdding(true); }}>
            <Plus /> New reservation
          </Button>
        )}
      />

      {known && !writable && <ReadOnlyNotice>Your role can view leases and reservations but not change them.</ReadOnlyNotice>}

      {netErr && (
        <Callout tone="warning" title="Client network names could not be loaded">
          {netErr}{" "}
          <Button variant="link" size="sm" onClick={loadNetworks}>Try again</Button>
        </Callout>
      )}

      <Tabs value={tab} onValueChange={(v) => setTab(v as "leases" | "reservations")}>
        <TabsList>
          <TabsTrigger value="leases">Leases {count(leaseList.total)}</TabsTrigger>
          <TabsTrigger value="reservations">Reservations {count(resList.total)}</TabsTrigger>
        </TabsList>

        <TabsContent value="leases" className="mt-4">
          <Card className="overflow-hidden">
            {searchBar}
            {leaseFailed ? (
              failed("leases", leaseList.err, () => void leaseList.reload())
            ) : leases === null ? (
              <SkeletonRows rows={5} cols={5} />
            ) : leases.length === 0 && !needle ? (
              <EmptyState size="sm" icon={<Wifi />} title="No active leases" hint="Leases appear here once clients connect." />
            ) : leases.length === 0 ? (
              noMatch("leases")
            ) : (
              <Table label="DHCP leases" stickyHeader maxHeight="36rem">
                <THead>
                  <TR>
                    <TH>IP address</TH>
                    <TH className="hidden sm:table-cell">MAC address</TH>
                    <TH className="hidden md:table-cell">Hostname</TH>
                    <TH className="hidden lg:table-cell">Client network</TH>
                    <TH>State</TH>
                    <TH className="hidden sm:table-cell">Expires</TH>
                    {writable && <TH><span className="sr-only">Actions</span></TH>}
                  </TR>
                </THead>
                <TBody>
                  {leases.map((l) => {
                    const st = leaseState(l.state);
                    const s = LEASE_STATE[st] ?? { label: `Other (${st})`, tone: "default" as const };
                    const net = netOf(l["ip-address"]);
                    return (
                      <TR key={`${l["ip-address"]}|${l["hw-address"]}`}>
                        <TD nowrap className="font-mono text-xs">
                          {l["ip-address"]}
                          <div className="font-mono text-caption text-muted-foreground sm:hidden">{l["hw-address"]}</div>
                          <div className="font-sans text-caption text-muted-foreground sm:hidden"><LeaseExpiry lease={l} now={now} /></div>
                        </TD>
                        <TD nowrap className="hidden font-mono text-xs sm:table-cell">{l["hw-address"]}</TD>
                        <TD truncate muted className="hidden md:table-cell" title={l.hostname || undefined}>{l.hostname || "—"}</TD>
                        <TD truncate muted className="hidden lg:table-cell" title={net?.name}>
                          {net ? net.name : networks === null && !netErr ? "…" : `Subnet ${l["subnet-id"] ?? "—"}`}
                        </TD>
                        <TD nowrap><StatusBadge tone={s.tone}>{s.label}</StatusBadge></TD>
                        <TD nowrap muted className="hidden sm:table-cell"><LeaseExpiry lease={l} now={now} /></TD>
                        {writable && (
                          <TD className="w-0">
                            <RowActions
                              subject={`lease ${l["ip-address"]}`}
                              primary={{ label: "Reserve this address", onSelect: () => reserve(l) }}
                            />
                          </TD>
                        )}
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            )}
            {leases && leases.length > 0 && (leaseList.offset > 0 || leaseList.hasMore) && (
              <CardFooter>
                <Pagination
                  offset={leaseList.offset}
                  limit={leaseList.pageSize}
                  shown={leases.length}
                  total={leaseList.total}
                  hasMore={leaseList.hasMore}
                  onChange={leaseList.setOffset}
                />
              </CardFooter>
            )}
          </Card>
        </TabsContent>

        <TabsContent value="reservations" className="mt-4">
          <Card className="overflow-hidden">
            {searchBar}
            {resFailed ? (
              failed("reservations", resList.err, loadReservations)
            ) : reservations === null ? (
              <SkeletonRows rows={4} cols={5} />
            ) : reservations.length === 0 && !needle ? (
              <EmptyState
                size="sm"
                icon={<Pin />}
                title="No reservations"
                hint="Pin a device — a printer, a TV or a door lock — to a fixed address."
                action={writable ? <Button size="sm" variant="secondary" onClick={() => { setPrefill(null); setAdding(true); }}><Plus /> New reservation</Button> : undefined}
              />
            ) : reservations.length === 0 ? (
              noMatch("reservations")
            ) : (
              <>
              {resList.err && (
                // The rows on screen are the last ones read: a reload that failed is said, not hidden behind them.
                <Callout tone="warning" title="The reservations could not be refreshed" className="m-3">
                  <span className="flex flex-wrap items-center gap-2">
                    {errMsg(resList.err)} The list shows the last reservations read.
                    <Button variant="secondary" size="sm" onClick={loadReservations}><RefreshCw /> Try again</Button>
                  </span>
                </Callout>
              )}
              <ReservationTable
                reservations={reservations}
                writable={writable}
                networkName={netName}
                onEdit={setEditRes}
                onRemove={setRemoving}
              />
              </>
            )}
            {reservations && reservations.length > 0 && (resList.offset > 0 || resList.hasMore) && (
              <CardFooter>
                <Pagination
                  offset={resList.offset}
                  limit={resList.pageSize}
                  shown={reservations.length}
                  total={resList.total}
                  hasMore={resList.hasMore}
                  onChange={resList.setOffset}
                />
              </CardFooter>
            )}
          </Card>
          {writable && (
            <p className="mt-2 text-caption text-muted-foreground">
              Reservation changes are staged. <Link href="/network" className="font-medium underline">Apply them on Client networks</Link>.
            </p>
          )}
        </TabsContent>
      </Tabs>

      {writable && (
        <>
          <AddReservationDialog
            open={adding}
            onOpenChange={(v) => { setAdding(v); if (!v) setPrefill(null); }}
            networks={networks ?? []}
            initial={prefill}
            onSaved={() => { toast.success("Reservation staged", STAGED_NOTE); setTab("reservations"); loadReservations(); }}
          />
          <EditReservationDialog
            reservation={editRes}
            networkName={editRes ? netName(editRes.guest_network_id) : undefined}
            onClose={() => setEditRes(null)}
            onSaved={() => { toast.success("Reservation change staged", STAGED_NOTE); loadReservations(); }}
          />
          <RemoveReservationDialog
            reservation={removing}
            onClose={() => setRemoving(null)}
            onRemoved={() => { toast.success("Removal staged", STAGED_NOTE); loadReservations(); }}
          />
        </>
      )}
    </PageShell>
  );
}
