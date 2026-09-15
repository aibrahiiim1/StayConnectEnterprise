"use client";

// PMS CONNECTION — the property management system this appliance talks to.
//
// This is the page an operator opens when guests cannot get online and nobody knows why. It has to answer
// four questions without them having to know which one to ask:
//
//   is it connected?            transport, continuity and sync, each stated separately because they fail
//                               separately and each has a different response;
//   is it keeping up?           the ingestion backlog, including how OLD the oldest waiting event is — a
//                               large backlog is a busy morning, an old one is a stuck processor;
//   what is it running?         the PUBLISHED Revision, which is the one the interface points at and not
//                               simply the newest one somebody created;
//   who does it serve?          the guest networks that route to it.
//
// WHAT CHANGED, and why.
//
// The four answers were all present and all BEHIND A CLICK. The list showed a lifecycle badge ("In use") and a
// configuration badge, and nothing else — so an interface whose socket had dropped an hour ago looked identical
// to a healthy one until you pressed Open. The single most important fact this product holds, "can a guest sign
// in with their room number right now", was three interactions deep.
//
// So the list now leads with live state: whether room sign-in works, whether the guest list is loading, and when
// the PMS was last heard from. A sync in progress is visible on the list row rather than inside a panel. Resync
// is reachable from the row.
//
// The forms moved into dialogs. Creating an interface pushed a card above the table, editing pushed another
// below it, and the lifecycle confirmation pushed a third between them — on a long list all three could open
// off-screen, and with two open at once nothing said which interface a Save applied to.
//
// The two actions here — publishing a Revision and rotating the credential — both change what happens to
// every subsequent guest, so both take a password confirmation and a reason, and publishing also carries the
// Revision the operator believed was live so a concurrent change is refused rather than silently reverted.
//
// The credential is never displayed, because there is no endpoint that returns it. This page can set one; it
// cannot show one, and it does not pretend to by rendering a masked placeholder that implies a value is
// being held somewhere it could be read from.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { SynchronizationCard } from "./synchronization-card";
import {
  api, PmsInterface, PmsInterfaceHealth, PmsRevision, PmsGuestNetworkRoute,
  PmsConnectionSettings, RosterReconciliationState,
} from "@/lib/api";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle, Section } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import {
  DialogForm, ConfirmDialog, Dialog, DialogContent, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Explain } from "@/components/ui/tooltip";
import { DList, MonoId, Separator, SkeletonRows, Metric } from "@/components/ui/misc";
import { formatRelative, formatDate } from "@/lib/utils";
import { describePmsReadiness } from "@/lib/health-words";
import { Hotel, Plus, RefreshCw, Router } from "lucide-react";

// toneFor maps a status word to a colour. UNKNOWN is deliberately "warn" and not "default": an interface we
// have never heard from is not a neutral state, it is one somebody needs to look at.
const toneFor = (s: string) =>
  ["CONNECTED", "CONTINUOUS", "IN_SYNC"].includes(s) ? "ok"
    : ["UNKNOWN", "RESYNC_REQUIRED", "RESYNCING", "RESYNC_IN_PROGRESS"].includes(s) ? "warn"
      : ["DISCONNECTED", "GAP_DETECTED", "OUT_OF_SYNC"].includes(s) ? "err" : "default";

const pretty = (s: string) => s.replace(/_/g, " ").toLowerCase();

// OPERATOR WORDING for the four freshness axes and the lifecycle.
//
// `pretty()` turned RESYNC_REQUIRED into "resync required" and GAP_DETECTED into "gap detected", which is
// the internal vocabulary with the underscores taken out. The axes answer four different questions and the
// words below say which: is the link up, is the feed continuous, is our copy complete, and is the interface
// switched on at all. Unknown values fall through to pretty() rather than being hidden.
const TRANSPORT_WORDS: Record<string, string> = {
  CONNECTED: "Connected",
  DISCONNECTED: "Not connected",
  UNKNOWN: "Never connected",
};
const CONTINUITY_WORDS: Record<string, string> = {
  CONTINUOUS: "Receiving updates",
  GAP_DETECTED: "Updates were missed",
  UNKNOWN: "No updates yet",
};
const SYNC_WORDS: Record<string, string> = {
  IN_SYNC: "Up to date",
  RESYNC_REQUIRED: "Needs a full refresh",
  RESYNC_IN_PROGRESS: "Refreshing now",
  RESYNCING: "Refreshing now",
  OUT_OF_SYNC: "Out of date",
  UNKNOWN: "Not yet loaded",
};
const LIFECYCLE_WORDS: Record<string, string> = {
  ACTIVE: "In use",
  AUTH_DISABLED: "Not in use",
  DRAINING: "Winding down",
  DECOMMISSIONED: "Retired",
};
const words = (map: Record<string, string>, v?: string) => (v ? map[v] ?? pretty(v) : "—");

// The bounded reasons room sign-in can be closed, in words. Each one is the server's own code; the sentence is
// what an operator does about it.
const ROOM_AUTH_WORDS: Record<string, string> = {
  INTERFACE_NOT_ACTIVE: "This connection is not in use yet — put it into use to start serving room sign-in.",
  NO_PUBLISHED_REVISION: "No configuration has been put live for this connection.",
  CONTINUITY_GAP: "Updates from the PMS were missed, so the guest list cannot be trusted until a full refresh.",
  CONTINUITY_NOT_ESTABLISHED: "No updates have been received yet, so there is nothing to verify guests against.",
  NOT_IN_SYNC: "The guest list is not up to date. A full refresh will fix it.",
  FEED_SILENT: "The PMS has gone quiet for longer than this connection allows.",
  REVISION_NOT_PINNED: "The live configuration changed and the connection has not picked it up yet.",
  MIRROR_NEVER_SYNCHRONIZED: "No complete guest list has ever been loaded, so there is nothing to fall back on.",
  RESYNC_IN_FLIGHT: "A full refresh is partway through. Room sign-in resumes when it finishes.",
  MATERIALIZATION_BEHIND: "The guest list has arrived and is being applied — usually a few seconds.",
};

// While a sync is in one of these stages, something is actively happening.
const ACTIVE_STAGES = new Set(["REQUESTING_FULL_SYNC", "WAITING_FOR_PMS", "RECEIVING", "PUBLISHING", "APPLYING"]);
const isSyncing = (h?: PmsInterfaceHealth | null) => {
  if (!h) return false;
  const stage = h.sync_stage === "COMPLETE" && h.materialization_ready === false ? "APPLYING" : h.sync_stage ?? "";
  return ACTIVE_STAGES.has(stage);
};

// Connector kinds in the words a hotel uses. Unknown kinds fall through to the raw value rather than being
// hidden: an Interface created before the canonical set was narrowed still has to be identifiable.
const CONNECTOR_LABELS: Record<string, string> = {
  "protel-fias": "Protel (FIAS)",
};

export default function PMSInterfacesPage() {
  const [rows, setRows] = useState<PmsInterface[] | null>(null);
  // Live health per interface, so the LIST can answer "is it working" without the operator opening anything.
  const [health, setHealth] = useState<Record<string, PmsInterfaceHealth>>({});
  const [err, setErr] = useState<unknown>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [authoring, setAuthoring] = useState<string | null>(null);
  // The current configuration of the interface being edited, loaded before the form opens so it can start
  // from what is actually in use. null while it loads or when there is nothing published yet.
  const [authoringInitial, setAuthoringInitial] = useState<Record<string, unknown> | null>(null);
  const [note, setNote] = useState<string | null>(null);
  // The pending lifecycle change, held while the operator confirms. Both directions are consequential —
  // one opens a live connection to the property's PMS, the other stops every guest on every mapped network
  // being resolved — so neither happens on a single click.
  const [lifecycle, setLifecycle] = useState<{ id: string; label: string; to: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ interfaces: PmsInterface[] }>("/pms-interfaces");
      const list = r.interfaces ?? [];
      setRows(list);
      setErr(null);
      // Health is fetched per interface because that is the shape edged serves. There are a handful of
      // interfaces at a property, so this is a handful of small reads — and it buys the list its entire reason
      // for existing.
      const results = await Promise.all(
        list
          .filter((i) => i.lifecycle_state !== "DECOMMISSIONED")
          .map(async (i) => {
            try {
              const h = await api.get<{ health: PmsInterfaceHealth }>(`/pms-interfaces/${i.id}/health`);
              return [i.id, h.health] as const;
            } catch {
              return null;
            }
          }),
      );
      setHealth(Object.fromEntries(results.filter(Boolean) as [string, PmsInterfaceHealth][]));
    } catch (e) {
      setErr(e);
      setRows([]);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // A SLOW POLL WHILE MOUNTED, FASTER WHILE ANYTHING IS SYNCING. The operator watching a guest list load should
  // not have to press refresh to find out whether it finished, and the page should notice a connection dropping
  // without being reopened.
  const anySyncing = Object.values(health).some(isSyncing);
  useEffect(() => {
    const t = setInterval(() => void load(), anySyncing ? 4000 : 20000);
    return () => clearInterval(t);
  }, [anySyncing, load]);

  // openEditor loads the CURRENT published configuration and then opens the form on it. If nothing is
  // published there is nothing to start from, and the form opens on its defaults — which is the only case
  // where that is the right behaviour.
  async function openEditor(id: string) {
    setNote(null); setActionErr(null);
    try {
      const r = await api.get<{ revisions: PmsRevision[] }>(`/pms-interfaces/${id}/revisions`);
      const live = (r.revisions ?? []).find((x) => x.published);
      setAuthoringInitial(live ? { ...(live.config ?? {}), source_timezone: live.source_timezone } : null);
    } catch {
      setAuthoringInitial(null);
    }
    setAuthoring(id);
  }

  async function changeLifecycle({ reason, password }: { reason: string; password: string }) {
    if (!lifecycle) return;
    setBusy(true); setActionErr(null);
    try {
      await api.post(`/pms-interfaces/${lifecycle.id}/lifecycle`, {
        state: lifecycle.to, reason_code: reason, password,
      });
      setNote(
        lifecycle.to === "ACTIVE"
          ? "This PMS connection is now in use. It may take a moment to connect and load the guest list."
          : "This PMS connection is no longer in use. Guests on networks routed to it can no longer sign in with their room details.",
      );
      setLifecycle(null);
      await load();
    } catch (e) {
      setActionErr(e);
    } finally {
      setBusy(false);
    }
  }

  const list = rows ?? [];
  const active = list.filter((i) => i.lifecycle_state === "ACTIVE");
  const ready = active.filter((i) => health[i.id]?.room_auth_ready).length;
  const inHouse = Object.values(health).reduce((a, h) => a + (h.in_house_stays ?? 0), 0);
  const backlog = Object.values(health).reduce((a, h) => a + (h.pending_events ?? 0), 0);

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Property management system"
        title="PMS connection"
        description="The link to the hotel's property management system. It is what lets a guest get online by typing their room number and name, and it is where the appliance's copy of the guest list comes from."
        actions={
          <>
            <Button variant="secondary" size="sm" onClick={() => void load()}>
              <RefreshCw /> Refresh
            </Button>
            <Button size="sm" onClick={() => { setCreating(true); setNote(null); setActionErr(null); }}>
              <Plus /> Add connection
            </Button>
          </>
        }
      />

      <ErrorBanner err={err} />
      {note && <Callout tone="success">{note}</Callout>}

      {rows !== null && list.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard
            label="Room sign-in"
            value={
              active.length === 0 ? (
                <Badge tone="default" className="text-sm">Not in use</Badge>
              ) : (
                <Badge tone={ready === active.length ? "ok" : ready > 0 ? "warn" : "err"} className="text-sm" dot>
                  {ready === active.length ? "Working" : ready > 0 ? "Partly working" : "Not working"}
                </Badge>
              )
            }
            tone={ready === active.length && active.length > 0 ? "ok" : "warn"}
            icon={<Hotel />}
            hint={
              active.length === 0
                ? "No connection is in use, so nobody can sign in with a room number."
                : `${ready} of ${active.length} connection${active.length === 1 ? "" : "s"} can verify guests right now.`
            }
          />
          <StatCard label="Guests in house" value={inHouse.toLocaleString()} hint="As the PMS last told us" />
          <StatCard
            label="Waiting to be applied"
            value={backlog.toLocaleString()}
            tone={backlog > 0 ? "warn" : "default"}
            hint="Messages received but not yet in the guest list"
          />
          <StatCard
            label="Guest list refresh"
            value={
              anySyncing ? (
                <Badge tone="info" className="text-sm" dot>In progress</Badge>
              ) : (
                <span className="text-lg">
                  {formatRelative(
                    Object.values(health)
                      .map((h) => h.last_complete_sync_at)
                      .filter(Boolean)
                      .sort()
                      .reverse()[0] ?? null,
                  )}
                </span>
              )
            }
            tone={anySyncing ? "info" : "default"}
            hint={anySyncing ? "The full guest list is being loaded." : "Last completed full load"}
          />
        </div>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={2} cols={5} />
          ) : list.length === 0 ? (
            <EmptyState
              icon={<Hotel />}
              title="No PMS connection is configured"
              hint="Without one, guests cannot sign in with a room number. Vouchers and username-and-password accounts still work."
              action={<Button onClick={() => setCreating(true)}><Plus /> Add a connection</Button>}
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Connection</TH>
                  <TH>Can guests sign in?</TH>
                  <TH>Link</TH>
                  <TH>Guest list</TH>
                  <TH>Backlog</TH>
                  <TH />
                </TR>
              </THead>
              <tbody>
                {list.map((i) => {
                  const h = health[i.id];
                  const readiness = describePmsReadiness({
                    transport: h?.transport_status,
                    sync: h?.sync_status,
                    roomAuthReady: h?.room_auth_ready,
                    inHouse: h?.in_house_stays,
                  });
                  const syncing = isSyncing(h);
                  const retired = i.lifecycle_state === "DECOMMISSIONED";
                  return (
                    <TR key={i.id}>
                      <TD>
                        <div className="flex items-center gap-2">
                          <StatusDot
                            tone={
                              retired || i.lifecycle_state !== "ACTIVE" ? "default"
                                : h?.room_auth_ready ? "ok"
                                  : h?.transport_status === "CONNECTED" ? "warn" : "err"
                            }
                          />
                          <span className="font-medium">{i.display_label || "Unnamed connection"}</span>
                        </div>
                        <div className="mt-0.5 text-xs text-muted-foreground">
                          {CONNECTOR_LABELS[i.connector_kind] ?? i.connector_kind}
                          {i.endpoint ? ` · ${i.endpoint}` : ""}
                        </div>
                      </TD>
                      <TD className="max-w-72">
                        {i.lifecycle_state !== "ACTIVE" ? (
                          <Badge tone="default">{words(LIFECYCLE_WORDS, i.lifecycle_state)}</Badge>
                        ) : (
                          <>
                            <Badge tone={h?.room_auth_ready ? "ok" : readiness.tone === "err" ? "err" : "warn"}>
                              {h?.room_auth_ready ? "Yes" : "No"}
                            </Badge>
                            {!h?.room_auth_ready && h?.room_auth_reason && (
                              <div className="mt-0.5 text-xs text-muted-foreground">
                                {ROOM_AUTH_WORDS[h.room_auth_reason] ?? pretty(h.room_auth_reason)}
                              </div>
                            )}
                          </>
                        )}
                      </TD>
                      <TD>
                        <Badge tone={toneFor(h?.transport_status ?? "UNKNOWN") as any}>
                          {words(TRANSPORT_WORDS, h?.transport_status)}
                        </Badge>
                        <div className="mt-0.5 text-2xs text-muted-foreground">
                          {h?.last_heartbeat_at
                            ? `Heard from ${formatRelative(h.last_heartbeat_at)}`
                            : h?.disconnected_since
                              ? `Down since ${formatRelative(h.disconnected_since)}`
                              : "—"}
                        </div>
                      </TD>
                      <TD>
                        {syncing ? (
                          <Badge tone="info" dot>Loading now</Badge>
                        ) : (
                          <Badge tone={toneFor(h?.sync_status ?? "UNKNOWN") as any}>
                            {words(SYNC_WORDS, h?.sync_status)}
                          </Badge>
                        )}
                        <div className="mt-0.5 text-2xs text-muted-foreground">
                          {syncing
                            ? `${(h?.sync_records_received ?? 0).toLocaleString()} records received`
                            : `${(h?.in_house_stays ?? 0).toLocaleString()} in house`}
                        </div>
                      </TD>
                      <TD>
                        <div className="text-sm tabular">{(h?.pending_events ?? 0).toLocaleString()}</div>
                        {(h?.review_events ?? 0) > 0 && (
                          <Link href="/stay-events" className="text-2xs text-warning-subtle-foreground hover:underline">
                            {h!.review_events} need a decision
                          </Link>
                        )}
                      </TD>
                      <TD className="whitespace-nowrap text-right">
                        <Button
                          size="sm"
                          variant={selected === i.id ? "secondary" : "ghost"}
                          onClick={() => setSelected(selected === i.id ? null : i.id)}
                          aria-expanded={selected === i.id}
                        >
                          {selected === i.id ? "Hide" : "Manage"}
                        </Button>
                        {/*
                          A DECOMMISSIONED interface is in its terminal state: a revision authored against it
                          can never be published or dialled. Offering the button and refusing the save at the
                          backend would be correct but pointless -- the honest surface is not to offer it.
                        */}
                        {!retired && (
                          <Button size="sm" variant="ghost" onClick={() => void openEditor(i.id)}>Settings</Button>
                        )}
                        {/*
                          PUTTING THE INTERFACE INTO USE. Creating and publishing were both reachable from this
                          screen and activation was not, so an operator could complete every visible step and
                          still have a connection that never dialled: the connector only picks up interfaces in
                          the ACTIVE state, and nothing in the product could produce it.

                          Activation is refused without a published revision — there would be no endpoint to
                          dial — so the button is offered only once one exists.
                        */}
                        {i.lifecycle_state === "AUTH_DISABLED" && i.published && (
                          <Button
                            size="sm"
                            onClick={() => setLifecycle({ id: i.id, label: i.display_label, to: "ACTIVE" })}
                          >
                            Put into use
                          </Button>
                        )}
                        {i.lifecycle_state === "ACTIVE" && (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => setLifecycle({ id: i.id, label: i.display_label, to: "AUTH_DISABLED" })}
                          >
                            Take out of use
                          </Button>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {selected && (
        <InterfaceDetail
          key={selected}
          id={selected}
          iface={list.find((r) => r.id === selected)}
          health={health[selected]}
          onChanged={load}
        />
      )}

      <CreateInterfaceDialog
        open={creating}
        onOpenChange={setCreating}
        onDone={(msg) => { setCreating(false); setNote(msg); void load(); }}
      />

      <AuthorRevisionDialog
        key={authoring ?? "none"}
        open={authoring !== null}
        onOpenChange={(v) => { if (!v) { setAuthoring(null); setAuthoringInitial(null); } }}
        interfaceID={authoring}
        initial={authoringInitial}
        onDone={(msg) => { setAuthoring(null); setAuthoringInitial(null); setNote(msg); void load(); }}
      />

      <ConfirmDialog
        open={lifecycle !== null}
        onOpenChange={(v) => !v && setLifecycle(null)}
        title={
          lifecycle?.to === "ACTIVE"
            ? "Put this PMS connection into use?"
            : "Take this PMS connection out of use?"
        }
        description={
          lifecycle?.to === "ACTIVE"
            ? "The appliance will connect to the property management system and load the current guest list."
            : "Guests on networks routed to this connection will no longer be able to sign in with their room number and name. Stays already recorded are kept, and nobody already online is disconnected."
        }
        confirmLabel={lifecycle?.to === "ACTIVE" ? "Put into use" : "Take out of use"}
        confirmVariant={lifecycle?.to === "ACTIVE" ? "primary" : "danger"}
        busy={busy}
        error={actionErr}
        requireReason
        reasonPlaceholder={lifecycle?.to === "ACTIVE" ? "Commissioning" : "Maintenance"}
        requirePassword
        onConfirm={changeLifecycle}
      />
    </PageShell>
  );
}

function InterfaceDetail({
  id, iface, health, onChanged,
}: {
  id: string;
  iface?: PmsInterface;
  health?: PmsInterfaceHealth;
  onChanged: () => void | Promise<void>;
}) {
  const [revisions, setRevisions] = useState<PmsRevision[] | null>(null);
  // Whether the ORIGIN of each saved version could be read at all. "Nothing was recorded" and "the record
  // could not be read" are different facts and the screen must not merge them.
  const [provenance, setProvenance] = useState<string>("AVAILABLE");
  const [routes, setRoutes] = useState<PmsGuestNetworkRoute[]>([]);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const [r, d] = await Promise.all([
        api.get<{ revisions: PmsRevision[]; provenance_status?: string }>(`/pms-interfaces/${id}/revisions`),
        api.get<{ guest_networks: PmsGuestNetworkRoute[] }>(`/pms-interfaces/${id}`),
      ]);
      setRevisions(r.revisions ?? []);
      setProvenance(r.provenance_status ?? "AVAILABLE");
      setRoutes(d.guest_networks ?? []);
      setErr(null);
    } catch (e) {
      setErr(e);
      setRevisions([]);
    }
  }, [id]);

  useEffect(() => { void load(); }, [load]);

  return (
    <div className="space-y-4">
      <ErrorBanner err={err} />

      <HealthCard health={health} />
      <SynchronizationCard id={id} health={health ?? null} onRefreshed={onChanged} />
      <RevisionsCard
        id={id}
        iface={iface}
        revisions={revisions}
        provenanceStatus={provenance}
        onPublished={async () => { await load(); await onChanged(); }}
      />
      {/* The Credential card is not rendered. The supported connector's link carries no transport
          authentication (credential_mode=NONE), so there is no credential to set or rotate — and the card's
          "never set" warning described a missing secret that is not supposed to exist, which read as a
          misconfiguration on a correctly configured interface. The component and its endpoint are left in
          place for a future connector that genuinely authenticates. */}
      <RoutingCard routes={routes} />
      <ConnectionRecoveryCard id={id} />
      <AdvancedDiagnosticsCard />
    </div>
  );
}

function HealthCard({ health }: { health?: PmsInterfaceHealth | null }) {
  if (!health) return null;
  // The dimensions are shown side by side rather than collapsed into one word. "Degraded" tells an
  // operator nothing about what to do; "connected but out of sync" tells them to look at the resync.
  // Each label is the QUESTION the axis answers, and each value is in operator words.
  const dims: { label: string; text: string; raw: string; detail?: string | null; help: string }[] = [
    {
      label: "Connection",
      text: words(TRANSPORT_WORDS, health.transport_status),
      raw: health.transport_status,
      detail: health.transport_error_code || null,
      help: "Whether the appliance currently has a live link to the property management system.",
    },
    {
      label: "Live updates",
      text: words(CONTINUITY_WORDS, health.continuity_status),
      raw: health.continuity_status,
      help: "Whether the stream of check-ins and check-outs has been unbroken. A gap means the guest list may have missed something.",
    },
    {
      label: "Guest list",
      text: words(SYNC_WORDS, health.sync_status),
      raw: health.sync_status,
      detail: health.last_sync_failure_code || null,
      help: "Whether the appliance's copy of who is in house is complete and current.",
    },
  ];

  const readiness = describePmsReadiness({
    transport: health.transport_status,
    sync: health.sync_status,
    roomAuthReady: health.room_auth_ready,
    inHouse: health.in_house_stays,
  });

  return (
    <Card>
      <CardHeader><CardTitle>Connection status</CardTitle></CardHeader>
      <CardBody className="space-y-4">
        <Callout tone={readiness.tone === "ok" ? "success" : readiness.tone === "err" ? "danger" : "warning"}
          title={readiness.headline}>
          {readiness.summary}
        </Callout>

        <dl className="grid grid-cols-1 gap-4 sm:grid-cols-3">
          {dims.map((d) => (
            <div key={d.label}>
              <dt className="mb-1 inline-flex items-center gap-1 text-xs font-medium text-muted-foreground">
                {d.label}
                <Explain>{d.help}</Explain>
              </dt>
              <dd>
                <Badge tone={toneFor(d.raw) as any}>{d.text}</Badge>
                {/* The raw code stays available as a tooltip: it is what appears in the daemon log, so an
                    operator reading it to support can still quote the exact value. */}
                {d.detail && (
                  <span className="ml-2 text-xs text-muted-foreground" title={d.raw}>{d.detail}</span>
                )}
              </dd>
            </div>
          ))}
        </dl>

        <Separator />

        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <Metric label="Guests in house" value={(health.in_house_stays ?? 0).toLocaleString()} />
          <Metric
            label="Last message"
            value={<span className="text-sm">{formatRelative(health.last_stay_event_at)}</span>}
          />
          <Metric
            label="Waiting to apply"
            value={(health.pending_events ?? 0).toLocaleString()}
            tone={health.pending_events > 0 ? "warn" : "default"}
            // The age of the oldest waiting event is the number that distinguishes a busy morning from a
            // stuck processor, so it is stated rather than left to be inferred from the count.
            sub={health.oldest_pending_at ? `Oldest ${formatRelative(health.oldest_pending_at)}` : undefined}
          />
          <Metric
            label="Needs investigation"
            value={(health.review_events ?? 0).toLocaleString()}
            tone={health.review_events > 0 ? "warn" : "default"}
          />
        </div>

        {/* THE ONE THING THAT WOULD OTHERWISE NEED A DIAGNOSTIC PAGE TO DISCOVER.
            Reconciliation runs by itself and nearly always has nothing outstanding, which is why its screen
            is not day-to-day navigation. But when it DOES have something, an operator must not have to go
            looking: it is stated here, in the PMS area they already use, with a direct route to the detail. */}
        {(health.review_events ?? 0) > 0 ? (
          <Callout tone="warning" title="Reconciliation needs investigation">
            {health.review_events.toLocaleString()} recorded PMS message
            {health.review_events === 1 ? "" : "s"} could not be matched to a stay and still need an answer.
            Sign-in continues from the guest list already in use; what these messages would have changed has
            not been applied.{" "}
            <Link href="/pms-reconciliation" className="font-medium underline underline-offset-2">
              Open the diagnostic
            </Link>
          </Callout>
        ) : (
          <p className="text-xs text-muted-foreground">
            Reconciliation is up to date: every PMS message has been matched or answered. It runs
            automatically after each complete guest list — there is nothing to do here.
          </p>
        )}
      </CardBody>
    </Card>
  );
}

// CONNECTION RECOVERY — advanced configuration for the link, where the link is configured.
//
// These five numbers governed how the PMS connection retries after a drop, and they lived on a diagnostic
// page an administrator had no reason to open. They are configuration, not diagnosis, so they belong here.
//
// Each states what it does, what it currently is, and the situation that would justify changing it, because
// a number and a unit tell an administrator nothing about whether to touch it -- and for nearly every
// property the right answer is to leave all five alone.
const CONN_FIELDS: {
  key: Extract<keyof PmsConnectionSettings, string>;
  label: string;
  unit: string;
  what: string;
  when: string;
}[] = [
  {
    key: "backoff_min_ms",
    label: "Shortest wait before retrying",
    unit: "milliseconds",
    what: "After the PMS link drops, how soon the first reconnection attempt happens. Each further attempt waits a little longer, up to the maximum below.",
    when: "Raise it if the hotel's PMS complains about repeated connections during its nightly restart.",
  },
  {
    key: "backoff_max_ms",
    label: "Longest wait before retrying",
    unit: "milliseconds",
    what: "Attempts never get further apart than this, so a PMS that comes back at 3am is picked up within this long with nobody present. The appliance retries indefinitely — this caps the gap, never the number of tries.",
    when: "Lower it if the PMS restarts often and you want the guest list current again sooner.",
  },
  {
    key: "stable_reset_seconds",
    label: "Connection must hold this long to count as recovered",
    unit: "seconds",
    what: "Once a reconnection survives this long, the waiting resets to the shortest value.",
    when: "Raise it if the link reconnects and drops again within a minute or two.",
  },
  {
    key: "link_down_alert_seconds",
    label: "Report the link as down after",
    unit: "seconds",
    what: "How long the link may stay down before it is reported. This governs when a person is TOLD — nothing stops when the link drops, and guests keep signing in from the last good guest list.",
    when: "Lower it to hear about an outage sooner; raise it if nightly maintenance produces an alert nobody acts on.",
  },
  {
    key: "blocked_after_refusals",
    label: "Report reconciliation as blocked after",
    unit: "consecutive runs",
    what: "Reconciliation declines to act on an incomplete or contradictory guest list, which protects guests. After this many refusals in a row it is reported.",
    when: "Lower it to hear about a degrading feed sooner.",
  },
];

function ConnectionRecoveryCard({ id }: { id: string }) {
  // SCOPED TO THIS CONNECTION. These used to be read from, and written to, a site-wide route -- so the card
  // had to warn that editing it here also changed any other connection at the property. It no longer does,
  // because it no longer can.
  const [settings, setSettings] = useState<PmsConnectionSettings | null>(null);
  const [form, setForm] = useState<Record<string, number>>({});
  const [reason, setReason] = useState("");
  const [saved, setSaved] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setSettings(await api.get<PmsConnectionSettings>(`/pms-interfaces/${id}/connection-settings`));
    } catch {
      /* advanced configuration: absent on a connector that does not expose it */
    }
  }, [id]);
  useEffect(() => { void load(); }, [load]);

  async function save() {
    setBusy(true);
    try {
      const out = await api.put<{ config_version: number }>(
        `/pms-interfaces/${id}/connection-settings`, { ...form, reason });
      setSaved(out.config_version);
      setForm({});
      setReason("");
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "the settings were rejected");
    } finally {
      setBusy(false);
    }
  }

  if (!settings) return null;

  return (
    <Card>
      <CardHeader><CardTitle>Advanced configuration — connection recovery</CardTitle></CardHeader>
      <CardBody className="space-y-3">
        {err && <p className="text-sm text-err">{err}</p>}
        <p className="text-sm text-muted-foreground">
          The PMS link reconnects by itself and retries for as long as it takes. These bound how fast it
          retries and how long a problem may last before it is reported.{" "}
          <strong>Most properties never need to change any of them</strong> — every change is recorded with
          who made it and why.
        </p>
        {/* THE SCOPE WARNING IS GONE BECAUSE THE SCOPE CHANGED. It used to read "these apply to the whole
            site, not just this connection", which was true and awful: a setting on a connection page that
            silently governed a different connection. The key now carries the interface, so the honest
            sentence is the short one. */}
        <div className="rounded-md border border-dashed p-3 text-sm text-muted-foreground">
          <strong className="text-foreground">These apply to this connection only.</strong>{" "}
          Another PMS connection at this property keeps its own values and is unaffected by anything changed
          here.
        </div>
        <div className="grid gap-3 lg:grid-cols-2">
          {CONN_FIELDS.map((f) => (
            <div key={f.key} className="rounded-md border p-3">
              <label className="text-sm">
                <span className="block font-medium">{f.label}</span>
                <span className="mt-1 block text-xs text-muted-foreground">{f.what}</span>
                <div className="mt-2 flex items-center gap-2">
                  <input
                    type="number"
                    className="w-40 rounded-md border px-3 py-2 text-sm"
                    value={form[f.key] ?? settings[f.key] ?? ""}
                    onChange={(e) => setForm({ ...form, [f.key]: Number(e.target.value) })}
                    disabled={busy}
                  />
                  <span className="text-xs text-muted-foreground">{f.unit}</span>
                </div>
                <span className="mt-2 block text-xs text-muted-foreground">
                  <strong>Currently:</strong> {settings[f.key]} {f.unit}
                  {settings.is_default ? " (nobody has changed this one)" : ""}
                </span>
                <span className="mt-1 block text-xs text-muted-foreground">
                  <strong>Change it when:</strong> {f.when}
                </span>
              </label>
            </div>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <input
            className="min-w-[20rem] flex-1 rounded-md border px-3 py-2 text-sm"
            placeholder="Reason - recorded against this change"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            disabled={busy}
          />
          <Button onClick={() => void save()} disabled={busy || Object.keys(form).length === 0}>
            Save recovery settings
          </Button>
        </div>
        {saved && (
          <p className="text-sm text-ok">Saved as version {saved}. It applies on the next reconnect.</p>
        )}
      </CardBody>
    </Card>
  );
}

// ADVANCED DIAGNOSTICS, deliberately at the bottom and deliberately not in the menu.
//
// Both screens below describe machinery that runs without anyone. Neither has an action on it. A property
// where everything works never needs either, so putting them in the main navigation taught operators to
// check pages that are meant to be empty -- and an empty page checked daily is how a real warning gets
// skimmed past. They are reached from here, where somebody troubleshooting the PMS already is.
function AdvancedDiagnosticsCard() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Advanced diagnostics</CardTitle>
      </CardHeader>
      <CardBody className="space-y-3">
        <p className="text-sm text-muted-foreground">
          These are for investigating the PMS integration. Normal operation needs none of them — the status
          above already says whether guests can sign in and whether anything needs attention. Neither screen
          can change a stay, dispose of a case or run a reconciliation by hand; both only show what the
          automatic process has done.
        </p>
        <ul className="space-y-3">
          <li>
            <Link href="/roster-reconciliation" className="font-medium underline underline-offset-2">
              Roster reconciliation
            </Link>
            <p className="text-sm text-muted-foreground">
              How the guest list is kept identical to the hotel&rsquo;s: what the last comparison found, why a
              comparison was refused, and every run that has happened.
            </p>
          </li>
          <li>
            <Link href="/pms-reconciliation" className="font-medium underline underline-offset-2">
              Unresolved departures
            </Link>
            <p className="text-sm text-muted-foreground">
              Individual PMS messages that could not be matched to a stay, with the evidence each one is
              waiting for.
            </p>
          </li>
        </ul>
      </CardBody>
    </Card>
  );
}

// WHAT REASON CODES MEAN, in operator language. These are the values the publish path actually records;
// anything unrecognised is shown verbatim rather than guessed at.
const REASON_WORDS: Record<string, string> = {
  INITIAL_COMMISSIONING: "First set up when the connection was commissioned",
  DERIVED_SOURCE_FINGERPRINT: "Saved automatically once the PMS message format had been observed",
  CONFIG_CORRECTION: "A correction to the saved settings",
  ENDPOINT_CHANGE: "The PMS address was changed",
  TIMEOUT_TUNING: "Timeouts were adjusted",
};

// THE FIELDS AN OPERATOR WOULD CALL "THE CONNECTION SETTINGS".
const SETTING_WORDS: [string, string][] = [
  ["endpoint", "PMS address"],
  ["dial_timeout_ms", "Connect timeout (ms)"],
  ["read_timeout_ms", "Read timeout (ms)"],
  ["write_timeout_ms", "Write timeout (ms)"],
  ["heartbeat_interval_ms", "Heartbeat every (ms)"],
  ["heartbeat_timeout_ms", "Heartbeat timeout (ms)"],
  ["feed_freshness_ms", "Guest list considered stale after (ms)"],
  ["complete_sync_ms", "Full guest list refresh every (ms)"],
  ["resync_supported", "Can request a full guest list"],
];

// WHAT ACTUALLY CHANGED between one saved version and the one before it.
//
// Derived ONLY from stored values -- never narrated. Where two versions carry identical connection settings
// this returns nothing, and the caller says so plainly instead of inventing a difference. That case is real:
// on this property versions 1 and 2 are byte-identical as connection settings, and differ only in derived
// internal state.
function describeChange(cur: PmsRevision, prev?: PmsRevision): { field: string; from: string; to: string }[] {
  if (!prev) return [];
  const out: { field: string; from: string; to: string }[] = [];
  for (const [key, label] of SETTING_WORDS) {
    const a = prev.config?.[key];
    const b = cur.config?.[key];
    if (JSON.stringify(a) !== JSON.stringify(b)) {
      out.push({ field: label, from: a === undefined ? "not set" : String(a), to: b === undefined ? "not set" : String(b) });
    }
  }
  if (prev.source_timezone !== cur.source_timezone) {
    out.push({ field: "Hotel time zone", from: prev.source_timezone, to: cur.source_timezone });
  }
  return out;
}

// Differences that are NOT connection settings: derived or internal state that still produced a new saved
// version. Naming these is the difference between "nothing changed" (wrong, and it looks like a bug) and
// "the settings are the same; this is what moved".
function describeInternalChange(cur: PmsRevision, prev?: PmsRevision): string[] {
  if (!prev) return [];
  const out: string[] = [];
  if ((prev.source_fingerprint ?? "") !== (cur.source_fingerprint ?? "")) {
    out.push(
      (prev.source_fingerprint ?? "") === ""
        ? "The PMS message format was observed and recorded for the first time"
        : "The recorded PMS message format changed",
    );
  }
  if (prev.normalization_version !== cur.normalization_version) {
    out.push("The message-handling version changed");
  }
  if (prev.folio_identity_strategy !== cur.folio_identity_strategy) {
    out.push("The folio matching strategy changed");
  }
  return out;
}

function SettingsTable({ rev }: { rev: PmsRevision }) {
  return (
    <Table>
      <THead><TR><TH>Setting</TH><TH>Value</TH></TR></THead>
      <tbody>
        {SETTING_WORDS.map(([key, label]) => (
          <TR key={key}>
            <TD>{label}</TD>
            <TD>{rev.config?.[key] === undefined ? "—" : String(rev.config[key])}</TD>
          </TR>
        ))}
        <TR><TD>Hotel time zone</TD><TD>{rev.source_timezone || "—"}</TD></TR>
      </tbody>
    </Table>
  );
}

function VersionProvenance({ rev, unavailable }: { rev: PmsRevision; unavailable?: boolean }) {
  const when = rev.published_at ?? rev.authored_at;
  // NEVER INVENTED, and never conflated. Empty provenance has two causes that look identical in the data
  // and are opposite in meaning: nothing was ever recorded, or the record could not be read. Saying "not
  // recorded" when the truth is "could not be read" is how a completely dead provenance feature survived a
  // deployment unnoticed -- every version claimed its own origin had never been written down.
  if (unavailable) {
    return (
      <p className="text-xs text-warn">
        The record of how this version was saved could not be read just now, so it is not shown. This is a
        fault to report, not a sign that nothing was recorded — the configuration itself is unaffected.
      </p>
    );
  }
  if (!when && !rev.actor_id && !rev.reason_code) {
    return (
      <p className="text-xs text-muted-foreground">
        How this version came to be saved was not recorded.
      </p>
    );
  }
  return (
    <ul className="space-y-0.5 text-xs text-muted-foreground">
      {when && <li>Saved {new Date(when).toLocaleString()}</li>}
      {(rev.actor_label || rev.actor_id) && (
        <li>
          By {rev.actor_label || "an operator who is no longer on this appliance"}
          {!rev.actor_label && rev.actor_id ? ` (${rev.actor_id.slice(0, 8)}…)` : ""}
        </li>
      )}
      {rev.reason_code && <li>Reason: {REASON_WORDS[rev.reason_code] ?? rev.reason_code}</li>}
    </ul>
  );
}

// CURRENT CONFIGURATION is the whole of the normal view. The saved-version table used to sit here in full,
// so the routine question -- "what is this connection set to?" -- was answered by a list of everything it
// has ever been set to. Previous versions move behind History.
function RevisionsCard({
  id, iface, revisions, provenanceStatus, onPublished,
}: {
  id: string;
  iface?: PmsInterface;
  revisions: PmsRevision[] | null;
  /** AVAILABLE when the origin records were read; UNAVAILABLE when the read itself failed. */
  provenanceStatus?: string;
  onPublished: () => void | Promise<void>;
}) {
  const [historyOpen, setHistoryOpen] = useState(false);
  // PUBLISH/ROLLBACK IS PRESERVED, and lives with the previous versions it acts on. Putting an older
  // version back is exactly "roll back", so History is where it belongs -- the main view is about what is in
  // force, not about changing it.
  const [publishing, setPublishing] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  async function publish({ reason, password }: { reason: string; password: string }) {
    if (!publishing) return;
    setBusy(true);
    setErr(null);
    try {
      await api.post(`/pms-interfaces/${id}/publish`, {
        revision_id: publishing,
        // The version this operator believed was in use. If it changed while the form was open, edged
        // refuses rather than reverting whoever published in between.
        expected_revision_id: iface?.current_revision_id ?? "",
        reason_code: reason,
        password,
      });
      setPublishing(null);
      setHistoryOpen(false);
      await onPublished();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }
  const ordered = (revisions ?? []).slice().sort((a, b) => b.revision_no - a.revision_no);
  const current = ordered.find((r) => r.published) ?? ordered[0];
  const previous = ordered.filter((r) => r.id !== current?.id);

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-3">
        <CardTitle>Current configuration</CardTitle>
        {previous.length > 0 && (
          <Button variant="secondary" onClick={() => setHistoryOpen(true)}>
            History ({previous.length})
          </Button>
        )}
      </CardHeader>
      <CardBody className="space-y-3">
        {!current ? (
          <p className="text-sm text-muted-foreground">No configuration has been saved yet.</p>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone="ok">Version {current.revision_no}</Badge>
              <Badge tone="neutral">In use</Badge>
            </div>
            <VersionProvenance rev={current} unavailable={provenanceStatus === "UNAVAILABLE"} />
            <SettingsTable rev={current} />
          </>
        )}
      </CardBody>

      <Dialog open={historyOpen} onOpenChange={setHistoryOpen}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>Configuration history</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              Previous saved versions of this connection, newest first. They are kept permanently and cannot
              be edited or removed.
            </p>
            {previous.map((rev) => {
              const older = ordered.find((o) => o.revision_no === rev.revision_no - 1);
              const changes = describeChange(rev, older);
              const internal = describeInternalChange(rev, older);
              return (
                <div key={rev.id} className="rounded-md border p-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge tone="neutral">Version {rev.revision_no}</Badge>
                    <span className="text-xs text-muted-foreground">Previous</span>
                  </div>
                  <div className="mt-2"><VersionProvenance rev={rev} unavailable={provenanceStatus === "UNAVAILABLE"} /></div>

                  <div className="mt-2 text-sm">
                    {!older ? (
                      <p className="text-muted-foreground">The first saved configuration.</p>
                    ) : changes.length > 0 ? (
                      <>
                        <p className="font-medium">Changed from version {older.revision_no}:</p>
                        <ul className="mt-1 space-y-0.5">
                          {changes.map((c, i) => (
                            <li key={i} className="text-muted-foreground">
                              {c.field}: <span className="line-through">{c.from}</span> → {c.to}
                            </li>
                          ))}
                        </ul>
                      </>
                    ) : internal.length > 0 ? (
                      <>
                        <p className="font-medium">
                          The connection settings are identical to version {older.revision_no}.
                        </p>
                        <ul className="mt-1 space-y-0.5">
                          {internal.map((t, i) => (
                            <li key={i} className="text-muted-foreground">{t}</li>
                          ))}
                        </ul>
                      </>
                    ) : (
                      <p className="text-muted-foreground">
                        No difference in the stored connection settings from version {older.revision_no}, and
                        nothing else recorded that explains why it was saved.
                      </p>
                    )}
                  </div>

                  <details className="mt-2">
                    <summary className="cursor-pointer text-xs text-muted-foreground">
                      All settings in this version
                    </summary>
                    <div className="mt-2"><SettingsTable rev={rev} /></div>
                  </details>

                  <div className="mt-3">
                    <Button size="sm" variant="secondary"
                      onClick={() => { setErr(null); setPublishing(rev.id); }}>
                      Put this version back in use
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={publishing !== null}
        onOpenChange={(v) => !v && setPublishing(null)}
        title="Put this configuration live?"
        description="From this moment on, every guest is verified using these settings. The connection will reconnect with them."
        confirmLabel="Put live"
        busy={busy}
        error={err}
        requireReason
        reasonPlaceholder="CONFIG_UPDATE"
        requirePassword
        onConfirm={publish}
      />
    </Card>
  );
}

function RoutingCard({ routes }: { routes: PmsGuestNetworkRoute[] }) {
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Which Wi-Fi networks use this connection</CardTitle>
          <p className="mt-0.5 text-xs text-muted-foreground">
            A guest is only checked against this PMS if they are on one of these networks.
          </p>
        </div>
        <Link href="/pms-routing" className="text-xs text-muted-foreground hover:text-foreground">
          Change routing →
        </Link>
      </CardHeader>
      <CardBody>
        {routes.length === 0 ? (
          // An interface no network routes to is configured but unreachable by any guest — worth saying,
          // because it looks identical to a healthy interface everywhere else on this page.
          <Callout tone="warning" title="No Wi-Fi network points at this connection">
            It is configured and may well be connected, but no guest is ever checked against it. Point at least
            one guest network at it on{" "}
            <Link href="/pms-routing" className="underline underline-offset-2">Network routing</Link>.
          </Callout>
        ) : (
          <ul className="space-y-1.5">
            {routes.map((r) => (
              <li key={r.guest_network_id} className="flex items-center gap-2 text-sm">
                <Router className="size-3.5 shrink-0 text-muted-foreground" />
                <span>{r.guest_network_name || r.guest_network_id}</span>
                {r.is_default && <Badge tone="info">Site default</Badge>}
              </li>
            ))}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}

// CREATING AN INTERFACE, AND AUTHORING ITS CONFIGURATION.
//
// Neither existed. The screen could publish a revision and rotate a credential, but there was no way to
// create an interface and no way to author a revision -- so the ENDPOINT the connector dials, and every
// timeout, bound and mode it reads, could not be set from the product at all. Every interface on the
// DEVELOPMENT appliance existed because a seed script wrote it straight into the database.
//
// Every field below is one the running connector actually reads (pmsd.Revision.Validate and
// pgRepo.LoadInterface). None is decorative, and nothing here invents protocol behaviour: the numbers are
// starting points an operator is expected to change, not a description of any real PMS.
function CreateInterfaceDialog({
  open, onOpenChange, onDone,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onDone: (msg: string) => void;
}) {
  // ONE SUPPORTED CONNECTOR, AND THE SCREEN SAYS SO.
  //
  // The dropdown offered six kinds, carried over from the legacy scd provider list. pmsd supports one and
  // refuses the rest, so choosing "mews" produced an Interface that could be created, configured and
  // published — and then rejected by the connector, surfacing at the last step as a connection failure
  // rather than as a connector this build does not implement. edged now refuses the other kinds outright;
  // this is the screen matching that, not the only line of defence.
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  useEffect(() => { if (open) { setLabel(""); setErr(null); } }, [open]);

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      title="Add a PMS connection"
      description="It starts switched off with nothing configured. You will configure it and then put it into use — both separate, confirmed steps."
      submitLabel="Create connection"
      busy={busy}
      error={err}
      disabled={!label.trim()}
      onSubmit={async () => {
        setBusy(true); setErr(null);
        try {
          await api.post("/pms-interfaces", { connector_kind: "protel-fias", display_label: label.trim() });
          onDone("Connection created. Configure it with Settings, then put it into use.");
        } catch (e) { setErr(e); } finally { setBusy(false); }
      }}
    >
      <Field label="Property management system" hint="The only system this appliance can connect to today.">
        <div className="flex h-9 items-center rounded-md border border-border bg-surface px-3 text-sm">
          Protel (FIAS)
        </div>
      </Field>
      <Field label="Name" required hint="How this connection appears throughout the admin.">
        <Input value={label} required maxLength={120} placeholder="Front office Protel"
          onChange={(e) => setLabel(e.target.value)} />
      </Field>
    </DialogForm>
  );
}

function AuthorRevisionDialog({
  open, onOpenChange, interfaceID, initial, onDone,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  interfaceID: string | null;
  // The CURRENT configuration, so Edit means "change what is there" rather than "retype it".
  //
  // IT USED TO START BLANK. Every field was a hardcoded default, so an operator changing a single timeout
  // silently reset the endpoint, the time zone and every other value to whatever this file happened to
  // default to — and the form looked exactly the same either way. That is not an edit; it is a new
  // configuration wearing an edit's clothes.
  initial?: Record<string, unknown> | null;
  onDone: (msg: string) => void;
}) {
  // WHAT THIS FORM NO LONGER ASKS, and why each one was a way to be wrong rather than a setting:
  //
  //   folio identity   — a FINANCIAL determination about how the property's PMS reuses folio numbers. It
  //                      defaulted to GLOBALLY_UNIQUE here, so saving the form asserted something nobody had
  //                      verified. New revisions are authored UNSET, which is what keeps posting impossible
  //                      until the determination is genuinely made.
  //   credential mode  — defaulted to AUTH_KEY while the supported connector's link is unauthenticated, so
  //                      the default produced an interface waiting forever for a secret that cannot exist.
  //   normalization    — identifies how THIS BUILD parses the feed. Typing a different number does not
  //                      change any parsing; it mislabels the events recorded under it.
  //   resync supported — a property of the protocol adapter, not of the hotel.
  //   read-only        — fixed true; pmsd refuses anything else, so asking was a formality.
  //
  // The server stamps all five and ignores anything sent for them, so removing the inputs closes the gap
  // rather than merely hiding it.
  const pick = (k: string, dflt: number) => {
    const v = initial?.[k];
    return typeof v === "number" && Number.isFinite(v) ? v : dflt;
  };
  const pickStr = (k: string, dflt: string) => {
    const v = initial?.[k];
    return typeof v === "string" && v !== "" ? v : dflt;
  };
  const [f, setF] = useState({
    endpoint: pickStr("endpoint", ""), source_timezone: pickStr("source_timezone", "Africa/Cairo"),
    dial_timeout_ms: pick("dial_timeout_ms", 5000),
    read_timeout_ms: pick("read_timeout_ms", 15000),
    write_timeout_ms: pick("write_timeout_ms", 15000),
    heartbeat_interval_ms: pick("heartbeat_interval_ms", 30000),
    heartbeat_timeout_ms: pick("heartbeat_timeout_ms", 90000),
    feed_freshness_ms: pick("feed_freshness_ms", 120000),
    complete_sync_ms: pick("complete_sync_ms", 600000),
    financial_base_currency: pickStr("financial_base_currency", ""),
    financial_base_currency_exponent:
      typeof initial?.financial_base_currency_exponent === "number"
        ? String(initial.financial_base_currency_exponent) : "",
  });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [advanced, setAdvanced] = useState(false);

  const num = (k: string) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setF((p) => ({ ...p, [k]: Number(e.target.value) }));
  const numField = (k: string, label: string, hint?: string) => (
    <Field label={label} hint={hint} key={k}>
      <Input type="number" min={1} value={String((f as any)[k])} onChange={num(k)} required />
    </Field>
  );

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      title={initial ? "Connection settings" : "Configure this connection"}
      description={
        initial
          ? "These are the settings in use now. Saving records the change; it does not take effect until you put it live, which is a separate confirmed step."
          : "Saved as a draft. Putting it live is a separate, confirmed step. This connection is read-only — StayConnect never writes to the PMS."
      }
      submitLabel="Save settings"
      busy={busy}
      error={err}
      onSubmit={async () => {
        if (!interfaceID) return;
        setBusy(true); setErr(null);
        try {
          const exp = f.financial_base_currency_exponent;
          await api.post(`/pms-interfaces/${interfaceID}/revisions`, {
            ...f,
            // read_only is sent as true and is not offered as a choice: pmsd refuses any revision whose
            // read-only capability is absent or false, so a control here would only offer a rejection.
            read_only: true,
            financial_base_currency: f.financial_base_currency.trim() || undefined,
            financial_base_currency_exponent: exp === "" ? undefined : Number(exp),
          });
          onDone("Settings saved as a new version. Put it live from Configuration history to start using it.");
        } catch (e) { setErr(e); } finally { setBusy(false); }
      }}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="PMS address and port" required hint="The host and port the appliance dials.">
          <Input required placeholder="pms.hotel.local:5010" value={f.endpoint}
            onChange={(e) => setF({ ...f, endpoint: e.target.value })} />
        </Field>
        <Field label="PMS time zone" required hint="IANA name. Arrivals and departures are read in it.">
          <Input required placeholder="Africa/Cairo" value={f.source_timezone}
            onChange={(e) => setF({ ...f, source_timezone: e.target.value })} />
        </Field>
      </div>

      {/* Fixed facts of the supported connector, shown so the operator can SEE them rather than set
          them. An operator still needs to know the link is read-only and unauthenticated; what they do not
          need is a control that can only be set one way. */}
      <Callout tone="neutral" title="Fixed for this connector">
        <ul className="list-disc space-y-0.5 pl-4 text-xs">
          <li>Read-only — StayConnect never writes to the PMS.</li>
          <li>No credential required — the link is unauthenticated.</li>
          <li>Full guest-list refresh is supported.</li>
          <li>
            Folio identity is not set, so charging a room through the PMS stays disabled until it is determined
            from the property&rsquo;s own folio behaviour.
          </li>
        </ul>
      </Callout>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Currency for charges (optional)">
          <Input maxLength={3} placeholder="USD" value={f.financial_base_currency}
            onChange={(e) => setF({ ...f, financial_base_currency: e.target.value.toUpperCase() })} />
        </Field>
        <Field label="Currency decimal places (optional)" hint="Set with the currency, or leave both empty.">
          <Input type="number" min={0} max={4} value={f.financial_base_currency_exponent}
            onChange={(e) => setF({ ...f, financial_base_currency_exponent: e.target.value })} />
        </Field>
      </div>

      {/* THE TIMEOUTS ARE FOLDED AWAY. Seven millisecond fields were the bulk of this form and are the part an
          operator almost never changes; leaving them expanded made the two fields that matter — where to dial
          and in which time zone — look like two of nine equal settings. */}
      <div>
        <button
          type="button"
          onClick={() => setAdvanced((v) => !v)}
          className="text-sm font-medium text-muted-foreground transition-colors hover:text-foreground"
        >
          {advanced ? "Hide" : "Show"} timing settings
        </button>
        {advanced && (
          <div className="mt-3 grid gap-4 rounded-md border border-border bg-surface/40 p-4 sm:grid-cols-2">
            {numField("dial_timeout_ms", "Dial timeout (ms)")}
            {numField("read_timeout_ms", "Read timeout (ms)")}
            {numField("write_timeout_ms", "Write timeout (ms)")}
            {numField("heartbeat_interval_ms", "Keep-alive every (ms)")}
            {numField("heartbeat_timeout_ms", "Keep-alive timeout (ms)", "Must exceed the interval.")}
            {numField("feed_freshness_ms", "Guest data stale after (ms)")}
            {numField("complete_sync_ms", "Full refresh at least every (ms)")}
          </div>
        )}
      </div>
    </DialogForm>
  );
}

// RevisionSummary renders a revision's configuration in words instead of dumping the config object.
//
// The table cell used to hold `JSON.stringify(r.config)`. It was safe — edged redacts before it leaves the
// server — but safe is not the same as usable: an operator checking "is this PMS configured correctly?" was
// reading snake_case keys and millisecond integers, and the two facts they most need (where it dials, how
// stale is too stale) were buried among five timeouts.
//
// Anything unrecognised is still shown, as key/value pairs. Dropping unknown keys would make this view lie
// by omission the first time a revision carries a field this component has not been taught.
function RevisionSummary({ config }: { config: Record<string, unknown> }) {
  const ms = (v: unknown): string | null => {
    const n = typeof v === "number" ? v : Number(v);
    if (!Number.isFinite(n) || n <= 0) return null;
    if (n % 1000 !== 0) return `${n} ms`;
    const secs = n / 1000;
    if (secs % 3600 === 0) return `${secs / 3600} h`;
    if (secs % 60 === 0) return `${secs / 60} min`;
    return `${secs} s`;
  };
  const auth = (config.auth ?? {}) as Record<string, unknown>;
  const known = new Set([
    "endpoint", "auth", "dial_timeout_ms", "read_timeout_ms", "write_timeout_ms",
    "heartbeat_interval_ms", "heartbeat_timeout_ms", "feed_freshness_ms", "complete_sync_ms",
    "resync_supported",
  ]);
  const rows: [string, string][] = [];
  if (config.endpoint) rows.push(["Connects to", String(config.endpoint)]);
  const hb = ms(config.heartbeat_interval_ms);
  const hbTo = ms(config.heartbeat_timeout_ms);
  if (hb || hbTo) rows.push(["Keep-alive", [hb && `every ${hb}`, hbTo && `give up after ${hbTo}`].filter(Boolean).join(", ")]);
  const fresh = ms(config.feed_freshness_ms);
  if (fresh) rows.push(["Guest data treated as stale after", fresh]);
  const sync = ms(config.complete_sync_ms);
  if (sync) rows.push(["Full guest-list refresh at least every", sync]);
  const dial = ms(config.dial_timeout_ms);
  const read = ms(config.read_timeout_ms);
  if (dial || read) rows.push(["Timeouts", [dial && `connect ${dial}`, read && `read ${read}`].filter(Boolean).join(", ")]);
  rows.push(["Authentication", auth.credential_mode === "NONE" ? "None — the link is unauthenticated" : String(auth.credential_mode ?? "—")]);
  rows.push(["Direction", auth.read_only === false ? "Read and write" : "Read-only"]);
  if (config.resync_supported != null) {
    rows.push(["Full refresh", config.resync_supported ? "Supported" : "Not supported"]);
  }
  const extra = Object.keys(config).filter((k) => !known.has(k));

  return (
    <div className="max-w-md space-y-0.5 text-xs">
      {rows.map(([k, v]) => (
        <div key={k}><span className="text-muted-foreground">{k}:</span> {v}</div>
      ))}
      {extra.map((k) => (
        <div key={k}><span className="text-muted-foreground">{k}:</span> {String(config[k])}</div>
      ))}
    </div>
  );
}
