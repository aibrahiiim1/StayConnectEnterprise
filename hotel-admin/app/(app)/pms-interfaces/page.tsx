"use client";

// Never-configured connections are kept in a folded group so a half-finished setup is not read as a PMS link that is down.
// Readiness is the server's verdict (`room_auth_ready`, `roomSignInReadiness`); this page words it and does not re-derive it.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { api, type PmsInterfaceHealth, type Whoami } from "@/lib/api";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { Card, CardBody, SectionCard } from "@/components/ui/card";
import { FilterBar } from "@/components/ui/toolbar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { FilterChips } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { canWrite } from "@/lib/roles";
import { formatDate, formatRelative } from "@/lib/utils";
import { useRoomSignInReadiness } from "@/components/room-sign-in-readiness";
import type { RoomSignInReadiness } from "@/lib/pms-availability";
import type { AssessmentInterfaceSummary } from "@/lib/api/pms-routing";
import {
  type PmsConnection, type PmsProvider,
  PROTEL_FALLBACK, isNeverConfigured, isSyncing, lastCommunication, loadProviderCatalogue, pmsErrorText, providerFor,
} from "@/lib/api/pms-connections";
import { ConnectionCard, ProviderTile, providerLabel } from "./connection-card";
import { ConnectionSheet, type SheetTab } from "./connection-sheet";
import { AddConnectionWizard } from "./add-connection-wizard";
import { HelpList, HelpSection } from "@/components/help";
import { ChevronDown, ChevronRight, Hotel, Plus, RefreshCw } from "lucide-react";
import { usePoll } from "@/lib/use-poll";

type Filter = "all" | "attention" | "working";

export default function PMSInterfacesPage() {
  const [rows, setRows] = useState<PmsConnection[] | null>(null);
  const [health, setHealth] = useState<Record<string, PmsInterfaceHealth>>({});
  // Health arrives after the list; until it does the summary must not announce "not working".
  const [healthLoaded, setHealthLoaded] = useState(false);
  // The one network-aware room sign-in assessment; null = it could not be read (unknown, never an outage).
  const { readiness: signIn, assessment, reload: reloadReadiness } = useRoomSignInReadiness();
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
      const [, results] = await Promise.all([
        reloadReadiness(),
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
      ]);
      setHealth(Object.fromEntries(results.filter((x) => x && x[1]) as [string, PmsInterfaceHealth][]));
      setHealthLoaded(true);
    } catch (e) {
      setErr(e);
      setRows((x) => x ?? []);
    }
  }, [reloadReadiness]);

  useEffect(() => { void load(); }, [load]);

  useEffect(() => {
    void loadProviderCatalogue().then((c) => { setProviders(c.providers); setCatalogueAvailable(c.available); });
    void api.get<Whoami>("/auth/whoami").then((me) => setRoles(me?.roles ?? [])).catch(() => setRoles([]));
  }, []);

  // Poll faster while any guest list is loading.
  const anySyncing = Object.values(health).some(isSyncing);
  usePoll(() => load(), anySyncing ? 4000 : 20000);

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

  const routingFor = useCallback((i: PmsConnection): AssessmentInterfaceSummary | null =>
    assessment?.interfaces.find((x) => x.pms_interface_id === i.id) ?? null, [assessment]);

  // A connection needs attention for room sign-in only when it is serving a client network and cannot answer; one that serves no active network is not a fault.
  const needsAttention = (i: PmsConnection) => routingFor(i)?.state === "IMPAIRED" || (health[i.id]?.review_events ?? 0) > 0;
  const working = (i: PmsConnection) => i.lifecycle_state === "ACTIVE" && routingFor(i)?.state === "SERVING";
  const shown = live.filter((i) => filter === "all" || (filter === "attention" ? needsAttention(i) : working(i)));

  const selectedRow = list.find((r) => r.id === selected) ?? null;
  const open = (id: string, t: SheetTab = "overview") => { setTab(t); setSelected(id); };

  return (
    <PageShell width="wide">
      <PageHeader
        title="PMS connection"
        description="Links to the site's property management systems."
        help={
          <>
            <HelpSection title="What a PMS connection does">
              <p>
                The links to the site&rsquo;s property management systems. They let a guest get online with their
                room number and name, and they are where the appliance&rsquo;s copy of the guest list comes from.
              </p>
            </HelpSection>
            <HelpSection title="Reading this page">
              <HelpList
                items={[
                  <><strong>Room sign-in</strong> says whether guests can sign in with a room number right now, per client network where the routing is known.</>,
                  <><strong>Guests in house</strong> is the guest list mirrored from the PMS.</>,
                  <>Each card shows one connection: which system, whether it is on, which check room sign-in depends on is failing, which Wi-Fi networks use it and what configuration is live. Open a card for details.</>,
                  <>Connections that were started but never configured, or retired, are kept apart under <strong>Inactive / not configured</strong> so a half-finished setup is never read as a PMS link that is down.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Advanced diagnostics">
              <p>
                The links at the bottom are for investigating the PMS integration. Normal operation needs none of
                them — the cards already say whether guests can sign in and whether anything needs attention.
              </p>
              <HelpList
                items={[
                  <><strong>Roster reconciliation</strong> — how the guest list is kept identical to the PMS&rsquo;s: what the last comparison found, why a comparison was refused, and every run that has happened.</>,
                  <><strong>Unresolved departures</strong> — individual PMS messages that could not be matched to a stay, with the evidence each one is waiting for.</>,
                ]}
              />
            </HelpSection>
          </>
        }
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
        <StatGrid cols={4}>
          {["Connections", "Room sign-in", "Guests in house", "Last heard from a PMS"].map((label) => (
            <StatCard key={label} label={label} value={<Skeleton className="h-6 w-24" />} hint="Checking…" />
          ))}
        </StatGrid>
      )}

      {rows !== null && list.length > 0 && healthLoaded && (
        <StatGrid cols={4}>
          <StatCard
            label="Connections"
            value={<span>{active.length}<span className="text-sm font-normal text-muted-foreground"> active</span></span>}
            hint={`${live.length} set up${inactive.length ? ` · ${inactive.length} inactive` : ""}`}
          />
          <RoomSignInStat signIn={signIn} activeCount={active.length} ready={ready}
            fromLastRoster={active.some((i) => health[i.id]?.room_auth_ready && health[i.id]?.room_auth_source === "LAST_GOOD_ROSTER")} />
          <StatCard
            label="Guests in house"
            value={inHouse.toLocaleString()}
            hint={anySyncing ? "A guest list is loading now" : "Mirrored from the PMS"}
          />
          <StatCard
            label="Last heard from a PMS"
            value={
              heard
                ? <time dateTime={heard} title={formatDate(heard)}>{formatRelative(heard)}</time>
                : <span className="text-muted-foreground">Never</span>
            }
            hint={heard ? formatDate(heard) : "No PMS heard from yet"}
          />
        </StatGrid>
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
              hint="Without one, guests cannot sign in with a room number. Vouchers and client accounts still work."
              action={writable ? <Button onClick={() => setAdding(true)}><Plus /> Add connection</Button> : undefined}
            />
          </CardBody>
        </Card>
      ) : (
        <>
          {healthLoaded && active.length > 0 && (signIn.state === "down" || signIn.state === "partial") ? (
            <Callout tone={signIn.state === "down" ? "danger" : "warning"}
              title={signIn.state === "down" ? "Guests cannot sign in with a room number" : "Room sign-in is not working on every client network"}>
              {signIn.state === "down"
                ? `${signIn.reason[0]?.toUpperCase() ?? ""}${signIn.reason.slice(1)}.`
                : `Not working on: ${signIn.affected.map((a) => a.guestNetwork).join(", ")}.`}
            </Callout>
          ) : null}

          {live.length > 1 && (
            <FilterBar>
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
            </FilterBar>
          )}

          {live.length === 0 ? (
            <Callout tone="neutral" title="No connection is set up yet">
              {writable
                ? "The connections below were started but never configured. Continue one of them, or add a new connection."
                : "The connections below were started but never configured."}
            </Callout>
          ) : shown.length === 0 ? (
            <Card>
              <EmptyState size="sm" title="No connection matches this filter"
                action={<Button size="sm" variant="secondary" onClick={() => setFilter("all")}>Show all</Button>} />
            </Card>
          ) : (
            <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
              {shown.map((i) => (
                <ConnectionCard
                  key={i.id}
                  iface={i}
                  health={health[i.id]}
                  healthLoaded={healthLoaded}
                  provider={providerFor(i.connector_kind, providers)}
                  routing={routingFor(i)}
                  onOpen={() => open(i.id)}
                />
              ))}
            </div>
          )}

          {inactive.length > 0 && (
            <Card className="overflow-hidden">
              <button
                type="button"
                aria-expanded={showInactive}
                onClick={() => setShowInactive((v) => !v)}
                className="flex w-full items-center gap-2 px-4 py-3 text-start text-sm font-medium hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
              >
                {showInactive
                  ? <ChevronDown className="size-4 text-muted-foreground" aria-hidden />
                  : <ChevronRight className="size-4 text-muted-foreground rtl:rotate-180" aria-hidden />}
                Inactive / not configured
                <Badge tone="neutral" size="sm">{inactive.length}</Badge>
                <span className="ms-auto hidden text-xs font-normal text-muted-foreground sm:inline">
                  Not connected to any PMS
                </span>
              </button>
              {showInactive && (
                <ul className="divide-y divide-border border-t border-border">
                  {inactive.map((i) => {
                    const retired = i.lifecycle_state === "DECOMMISSIONED";
                    const p = providerFor(i.connector_kind, providers);
                    return (
                      <li key={i.id} className="flex flex-wrap items-center gap-3 px-4 py-2.5">
                        <ProviderTile label={providerLabel(i, p)} className="size-8 text-sm" />
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-medium">{i.display_label || "Unnamed connection"}</div>
                          <div className="text-xs text-muted-foreground">
                            {providerLabel(i, p)} ·{" "}
                            {retired ? "Retired — no longer used" : "Never configured — not connected to any PMS"}
                          </div>
                        </div>
                        <Button size="sm" variant={retired || !writable ? "ghost" : "secondary"}
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

// Diagnostics stay reachable from here rather than the sidebar; the operator is told here when one has something to show.
function AdvancedDiagnostics({ reviewEvents }: { reviewEvents: number }) {
  return (
    <SectionCard title="Advanced diagnostics" description="For investigating the PMS integration.">
      {reviewEvents > 0 && (
        <Callout tone="warning" title="Reconciliation needs investigation" className="mb-3">
          {reviewEvents.toLocaleString()} PMS message{reviewEvents === 1 ? "" : "s"} could not be matched to a
          stay. Sign-in continues from the guest list already in use.{" "}
          <Link href="/pms-reconciliation" className="font-medium underline underline-offset-2">Open the diagnostic</Link>
        </Callout>
      )}
      <ul className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
        <li>
          <Link href="/roster-reconciliation" className="text-sm font-medium text-primary hover:underline">
            Roster reconciliation
          </Link>
          <p className="text-xs text-muted-foreground">How the guest list is kept identical to the PMS&rsquo;s.</p>
        </li>
        <li>
          <Link href="/pms-reconciliation" className="text-sm font-medium text-primary hover:underline">
            Unresolved departures
          </Link>
          <p className="text-xs text-muted-foreground">PMS messages that could not be matched to a stay.</p>
        </li>
      </ul>
    </SectionCard>
  );
}

function RoomSignInStat({
  signIn, activeCount, ready, fromLastRoster,
}: {
  signIn: RoomSignInReadiness;
  activeCount: number;
  ready: number;
  /** A ready connection is answering from its last complete guest list rather than a live feed. */
  fromLastRoster: boolean;
}) {
  // Per guest network when routing is known; otherwise the per-connection count.
  if (activeCount === 0) {
    return <StatCard label="Room sign-in" value={<span className="text-muted-foreground">Not in use</span>} hint="No connection is active" />;
  }
  if (signIn.state === "none") {
    return <StatCard label="Room sign-in" value={<span className="text-muted-foreground">Not offered</span>} hint="No client network uses a PMS for sign-in" />;
  }
  if (signIn.state === "ready" && fromLastRoster) {
    return <StatCard label="Room sign-in" tone="warn" value="Working" hint="From the last complete guest list" />;
  }
  if (signIn.state === "ready") {
    return (
      <StatCard label="Room sign-in" tone="ok" value="Working"
        hint={signIn.unchecked.length ? `${signIn.unchecked.length} network${signIn.unchecked.length === 1 ? "" : "s"} not yet checked` : "On every client network using a PMS"} />
    );
  }
  if (signIn.state === "partial" || signIn.state === "down") {
    return (
      <StatCard label="Room sign-in" tone={signIn.state === "down" ? "err" : "warn"}
        value={signIn.state === "down" ? "Not working" : "Partly working"}
        hint={`${signIn.affected.length} client network${signIn.affected.length === 1 ? "" : "s"} affected`} />
    );
  }
  return (
    <StatCard label="Room sign-in" tone={ready === activeCount ? "ok" : ready > 0 ? "warn" : "err"}
      value={ready === activeCount ? "Working" : ready > 0 ? "Partly working" : "Not working"}
      hint={`${ready} of ${activeCount} active connection${activeCount === 1 ? "" : "s"} ready`} />
  );
}
