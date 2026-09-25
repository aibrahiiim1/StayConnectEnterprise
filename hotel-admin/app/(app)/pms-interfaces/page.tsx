"use client";

// PMS CONNECTIONS — every link between this appliance and a property management system.
//
// This is the page an operator opens when guests cannot get online with their room number and nobody knows why,
// so it leads with the answer: can guests sign in right now, how many guests does the appliance know about, and
// when was a PMS last heard from. Below that, one card per connection says the same for that connection alone —
// which system, whether it is on, which of the four checks room sign-in depends on is failing, which Wi-Fi
// networks use it and what configuration is live. Everything else opens in a side sheet over the list.
//
// Connections that were created and never configured are real rows, but they are not live connections: they are
// kept in their own folded group so a half-finished setup is never read as a PMS link that is down.
//
// Readiness is the server's verdict. `room_auth_ready` decides whether a connection can serve room sign-in, and
// `roomSignInReadiness` works out which guest networks that affects; this page words them and does not re-derive
// either.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, type PmsGuestNetworkRoute, type PmsInterfaceHealth, type Whoami } from "@/lib/api";
import { PageHeader, PageShell, StatCard, Toolbar } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { FilterChips } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { canWrite } from "@/lib/roles";
import { formatDate, formatRelative } from "@/lib/utils";
import { roomSignInReadiness } from "@/lib/pms-availability";
import {
  type PmsConnection, type PmsProvider,
  PROTEL_FALLBACK, isNeverConfigured, isSyncing, lastCommunication, loadProviderCatalogue, pmsErrorText, providerFor,
} from "@/lib/api/pms-connections";
import { ConnectionCard, ProviderTile, providerLabel } from "./connection-card";
import { ConnectionSheet, type SheetTab } from "./connection-sheet";
import { AddConnectionWizard } from "./add-connection-wizard";
import { ChevronDown, ChevronRight, Hotel, Plus, RefreshCw } from "lucide-react";

type Filter = "all" | "attention" | "working";

export default function PMSInterfacesPage() {
  const [rows, setRows] = useState<PmsConnection[] | null>(null);
  // Live health per interface, so each card can answer "is it working" without the operator opening anything.
  const [health, setHealth] = useState<Record<string, PmsInterfaceHealth>>({});
  // HEALTH ARRIVES AFTER THE LIST, AND UNTIL IT DOES NOTHING HERE KNOWS ANYTHING. Three states — not asked,
  // answered, failed — so the summary never announces "not working" merely because it has not asked yet.
  const [healthLoaded, setHealthLoaded] = useState(false);
  // Site routing, for "which guest networks does this connection serve". null = could not be read.
  const [routes, setRoutes] = useState<PmsGuestNetworkRoute[] | null>(null);
  const [err, setErr] = useState<unknown>(null);

  const [providers, setProviders] = useState<PmsProvider[]>([PROTEL_FALLBACK]);
  const [catalogueAvailable, setCatalogueAvailable] = useState(false);
  const [roles, setRoles] = useState<string[]>([]);

  const [selected, setSelected] = useState<string | null>(null);
  const [tab, setTab] = useState<SheetTab>("overview");
  const [adding, setAdding] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");
  const [showInactive, setShowInactive] = useState(false);

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ interfaces: PmsConnection[] }>("/pms-interfaces");
      const list = r.interfaces ?? [];
      setRows(list);
      setErr(null);
      const [results, rt] = await Promise.all([
        Promise.all(
          list
            .filter((i) => i.lifecycle_state !== "DECOMMISSIONED" && !isNeverConfigured(i))
            .map(async (i) => {
              try {
                const h = await api.get<{ health: PmsInterfaceHealth }>(`/pms-interfaces/${i.id}/health`);
                return [i.id, h.health] as const;
              } catch {
                return null;
              }
            }),
        ),
        api.get<{ routes?: PmsGuestNetworkRoute[] }>("/pms-routing").catch(() => null),
      ]);
      setRoutes(Array.isArray(rt?.routes) ? rt!.routes : null);
      setHealth(Object.fromEntries(results.filter((x) => x && x[1]) as [string, PmsInterfaceHealth][]));
      setHealthLoaded(true);
    } catch (e) {
      setErr(e);
      setRows((x) => x ?? []);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // The provider catalogue and the operator's role change rarely; they are read once.
  useEffect(() => {
    void loadProviderCatalogue().then((c) => { setProviders(c.providers); setCatalogueAvailable(c.available); });
    void api.get<Whoami>("/auth/whoami").then((me) => setRoles(me?.roles ?? [])).catch(() => setRoles([]));
  }, []);

  // A SLOW POLL WHILE MOUNTED, FASTER WHILE ANY GUEST LIST IS LOADING.
  const anySyncing = Object.values(health).some(isSyncing);
  useEffect(() => {
    const t = setInterval(() => void load(), anySyncing ? 4000 : 20000);
    return () => clearInterval(t);
  }, [anySyncing, load]);

  const writable = canWrite("pms-interfaces", roles);
  const list = rows ?? [];
  const live = list.filter((i) => !isNeverConfigured(i) && i.lifecycle_state !== "DECOMMISSIONED");
  const inactive = list.filter((i) => isNeverConfigured(i) || i.lifecycle_state === "DECOMMISSIONED");
  const active = list.filter((i) => i.lifecycle_state === "ACTIVE");
  const ready = active.filter((i) => health[i.id]?.room_auth_ready).length;
  const inHouse = Object.values(health).reduce((a, h) => a + (h.in_house_stays ?? 0), 0);
  const reviewEvents = Object.values(health).reduce((a, h) => a + (h.review_events ?? 0), 0);
  const heard = Object.values(health).map(lastCommunication).filter(Boolean)
    .sort((a, b) => new Date(b!).getTime() - new Date(a!).getTime())[0] ?? null;

  const networksFor = useCallback((i: PmsConnection): string[] | null => {
    if (!routes) return null;
    return routes
      .filter((r) => r.pms_interface_id === i.id ||
        (r.routing_mode === "ALL_ACTIVE_INTERFACES" && i.lifecycle_state === "ACTIVE"))
      .map((r) => r.guest_network_name || "Unnamed network");
  }, [routes]);

  const needsAttention = (i: PmsConnection) => {
    const h = health[i.id];
    return (i.lifecycle_state === "ACTIVE" && h && h.room_auth_ready === false) || (h?.review_events ?? 0) > 0;
  };
  const working = (i: PmsConnection) => i.lifecycle_state === "ACTIVE" && health[i.id]?.room_auth_ready === true;
  const shown = live.filter((i) => filter === "all" || (filter === "attention" ? needsAttention(i) : working(i)));

  // WHICH GUEST NETWORKS CAN SIGN GUESTS IN, per the routing — the site-wide answer to "is room sign-in working".
  const signIn = useMemo(
    () => roomSignInReadiness(list, healthLoaded ? Object.values(health) : null, routes),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rows, health, healthLoaded, routes],
  );

  const selectedRow = list.find((r) => r.id === selected) ?? null;
  const open = (id: string, t: SheetTab = "overview") => { setTab(t); setSelected(id); };

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<Hotel />}
        eyebrow="Property management system"
        title="PMS connection"
        description="The links to the hotel's property management systems. They let a guest get online with their room number and name, and they are where the appliance's copy of the guest list comes from."
        actions={
          <>
            <Button variant="secondary" size="sm" onClick={() => void load()}>
              <RefreshCw /> Refresh
            </Button>
            {writable && (
              <Button size="sm" onClick={() => setAdding(true)}>
                <Plus /> Add connection
              </Button>
            )}
          </>
        }
      />

      <ErrorBanner err={pmsErrorText(err)} />

      {rows !== null && list.length > 0 && !healthLoaded && (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {["Connections", "Room sign-in", "Guests in house", "Last heard from a PMS"].map((label) => (
            <StatCard key={label} label={label} value={<Skeleton className="h-6 w-24" />} hint="Checking the connections…" />
          ))}
        </div>
      )}

      {rows !== null && list.length > 0 && healthLoaded && (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard
            label="Connections"
            icon={<Hotel />}
            value={<span>{active.length}<span className="text-base font-normal text-muted-foreground"> active</span></span>}
            hint={`${live.length} set up${inactive.length ? ` · ${inactive.length} inactive or not configured` : ""}`}
          />
          <RoomSignInStat signIn={signIn} activeCount={active.length} ready={ready} />
          <StatCard
            label="Guests in house"
            value={inHouse.toLocaleString()}
            hint={anySyncing ? "A guest list is loading now" : "Mirrored from the PMS"}
            tone={anySyncing ? "info" : "default"}
          />
          <StatCard
            label="Last heard from a PMS"
            value={
              heard
                ? <time className="text-lg" dateTime={heard} title={formatDate(heard)}>{formatRelative(heard)}</time>
                : <span className="text-lg text-muted-foreground">Never</span>
            }
            hint={heard ? formatDate(heard) : "No PMS has been heard from yet"}
          />
        </div>
      )}

      {rows === null ? (
        <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
          {[0, 1].map((k) => <Skeleton key={k} className="h-72 w-full rounded-lg" />)}
        </div>
      ) : list.length === 0 ? (
        <Card>
          <CardBody>
            <EmptyState
              icon={<Hotel />}
              title="No PMS connection yet"
              hint="Without one, guests cannot sign in with a room number. Vouchers and guest accounts still work. Add a connection to link the appliance to the hotel's property management system."
              action={writable ? <Button onClick={() => setAdding(true)}><Plus /> Add connection</Button> : undefined}
            />
          </CardBody>
        </Card>
      ) : (
        <>
          {live.length > 1 && (
            <Toolbar>
              <FilterChips<Filter>
                label="Show connections"
                value={filter}
                onChange={setFilter}
                options={[
                  { value: "all", label: "All", count: live.length },
                  { value: "attention", label: "Needs attention", count: live.filter(needsAttention).length, tone: "warn" },
                  { value: "working", label: "Working", count: live.filter(working).length, tone: "ok" },
                ]}
              />
            </Toolbar>
          )}

          {live.length === 0 ? (
            <Callout tone="neutral" title="No connection is set up yet">
              {writable
                ? "The connections below were started but never configured. Continue one of them, or add a new connection."
                : "The connections below were started but never configured."}
            </Callout>
          ) : shown.length === 0 ? (
            <p className="text-sm text-muted-foreground">No connection matches this filter.</p>
          ) : (
            <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
              {shown.map((i) => (
                <ConnectionCard
                  key={i.id}
                  iface={i}
                  health={health[i.id]}
                  healthLoaded={healthLoaded}
                  provider={providerFor(i.connector_kind, providers)}
                  networks={networksFor(i)}
                  onOpen={() => open(i.id)}
                />
              ))}
            </div>
          )}

          {inactive.length > 0 && (
            <Card>
              <button
                type="button"
                aria-expanded={showInactive}
                onClick={() => setShowInactive((v) => !v)}
                className="flex w-full items-center gap-2 px-4 py-3 text-left text-sm font-medium"
              >
                {showInactive ? <ChevronDown className="size-4" aria-hidden /> : <ChevronRight className="size-4" aria-hidden />}
                Inactive / not configured
                <Badge tone="neutral">{inactive.length}</Badge>
                <span className="ml-auto hidden text-xs font-normal text-muted-foreground sm:inline">
                  Not connected to any PMS — kept apart from the live connections
                </span>
              </button>
              {showInactive && (
                <ul className="divide-y divide-border border-t border-border">
                  {inactive.map((i) => {
                    const retired = i.lifecycle_state === "DECOMMISSIONED";
                    const p = providerFor(i.connector_kind, providers);
                    return (
                      <li key={i.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                        <ProviderTile label={providerLabel(i, p)} className="size-8 text-sm" />
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-medium">{i.display_label || "Unnamed connection"}</div>
                          <div className="text-xs text-muted-foreground">
                            {providerLabel(i, p)} ·{" "}
                            {retired ? "Retired — no longer used" : "Never configured — not connected to any PMS"}
                          </div>
                        </div>
                        <Button size="sm" variant={retired ? "ghost" : "secondary"}
                          onClick={() => open(i.id, retired ? "overview" : "configuration")}>
                          {retired ? "View" : writable ? "Continue setup" : "View"}
                        </Button>
                      </li>
                    );
                  })}
                </ul>
              )}
            </Card>
          )}
        </>
      )}

      {list.length > 0 && <AdvancedDiagnostics reviewEvents={reviewEvents} />}

      <ConnectionSheet
        open={selected !== null && selectedRow !== null}
        onOpenChange={(v) => { if (!v) setSelected(null); }}
        iface={selectedRow}
        health={selected ? health[selected] : undefined}
        provider={selectedRow ? providerFor(selectedRow.connector_kind, providers) : null}
        writable={writable}
        tab={tab}
        onTab={setTab}
        onChanged={load}
      />

      <AddConnectionWizard
        open={adding}
        onOpenChange={setAdding}
        providers={providers}
        catalogueAvailable={catalogueAvailable}
        onChanged={load}
        onOpenConnection={(id) => open(id, "configuration")}
      />
    </PageShell>
  );
}

// ADVANCED DIAGNOSTICS — deliberately at the bottom and deliberately not in the menu (Product-Owner decision:
// these screens stay reachable from here rather than from the sidebar). Both describe machinery that runs
// without anyone and has no action on it; a property where everything works never needs either. When one of
// them DOES have something, the operator is told here rather than having to go looking.
function AdvancedDiagnostics({ reviewEvents }: { reviewEvents: number }) {
  return (
    <Card>
      <CardBody className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold">Advanced diagnostics</h2>
          <p className="text-sm text-muted-foreground">
            For investigating the PMS integration. Normal operation needs none of these — the cards above already
            say whether guests can sign in and whether anything needs attention.
          </p>
        </div>
        {reviewEvents > 0 && (
          <Callout tone="warning" title="Reconciliation needs investigation">
            {reviewEvents.toLocaleString()} PMS message{reviewEvents === 1 ? "" : "s"} could not be matched to a
            stay. Sign-in continues from the guest list already in use.{" "}
            <Link href="/pms-reconciliation" className="font-medium underline underline-offset-2">Open the diagnostic</Link>
          </Callout>
        )}
        <ul className="grid gap-3 sm:grid-cols-2">
          <li>
            <Link href="/roster-reconciliation" className="text-sm font-medium underline underline-offset-2">
              Roster reconciliation
            </Link>
            <p className="text-xs text-muted-foreground">
              How the guest list is kept identical to the hotel&rsquo;s: what the last comparison found, why a
              comparison was refused, and every run that has happened.
            </p>
          </li>
          <li>
            <Link href="/pms-reconciliation" className="text-sm font-medium underline underline-offset-2">
              Unresolved departures
            </Link>
            <p className="text-xs text-muted-foreground">
              Individual PMS messages that could not be matched to a stay, with the evidence each one is waiting for.
            </p>
          </li>
        </ul>
      </CardBody>
    </Card>
  );
}

function RoomSignInStat({
  signIn, activeCount, ready,
}: {
  signIn: ReturnType<typeof roomSignInReadiness>;
  activeCount: number;
  ready: number;
}) {
  // Per guest network when the routing is known; otherwise the per-connection count, which is still the
  // server's verdict for each connection.
  if (activeCount === 0) {
    return (
      <StatCard
        label="Room sign-in"
        value={<Badge tone="default" className="text-sm">Not in use</Badge>}
        hint="No connection is active, so nobody can sign in with a room number."
        icon={<Hotel />}
      />
    );
  }
  if (signIn.state === "ready") {
    return (
      <StatCard label="Room sign-in" tone="ok" icon={<Hotel />}
        value={<Badge tone="ok" className="text-sm" dot>Working</Badge>}
        hint={`Every guest network using a PMS can sign guests in${signIn.unchecked.length ? ` (${signIn.unchecked.length} not yet checked)` : ""}.`} />
    );
  }
  if (signIn.state === "partial" || signIn.state === "down") {
    const names = signIn.affected.map((a) => a.guestNetwork).join(", ");
    return (
      <StatCard label="Room sign-in" tone={signIn.state === "down" ? "err" : "warn"} icon={<Hotel />}
        value={<Badge tone={signIn.state === "down" ? "err" : "warn"} className="text-sm" dot>
          {signIn.state === "down" ? "Not working" : "Partly working"}
        </Badge>}
        hint={signIn.state === "down" ? `Guests cannot sign in with a room number: ${signIn.reason}.` : `Not working on: ${names}.`} />
    );
  }
  return (
    <StatCard label="Room sign-in" tone={ready === activeCount ? "ok" : "warn"} icon={<Hotel />}
      value={<Badge tone={ready === activeCount ? "ok" : ready > 0 ? "warn" : "err"} className="text-sm" dot>
        {ready === activeCount ? "Working" : ready > 0 ? "Partly working" : "Not working"}
      </Badge>}
      hint={`${ready} of ${activeCount} active connection${activeCount === 1 ? "" : "s"} can verify guests right now.`} />
  );
}
