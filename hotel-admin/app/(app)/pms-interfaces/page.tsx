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
// The two actions here — publishing a Revision and rotating the credential — both change what happens to
// every subsequent guest, so both take a password confirmation and a reason, and publishing also carries the
// Revision the operator believed was live so a concurrent change is refused rather than silently reverted.
//
// The credential is never displayed, because there is no endpoint that returns it. This page can set one; it
// cannot show one, and it does not pretend to by rendering a masked placeholder that implies a value is
// being held somewhere it could be read from.

import { useCallback, useEffect, useState } from "react";
import { SynchronizationCard } from "./synchronization-card";
import {
  api, PmsInterface, PmsInterfaceHealth, PmsRevision, PmsGuestNetworkRoute,
} from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative } from "@/lib/utils";

// toneFor maps a status word to a colour. UNKNOWN is deliberately "warn" and not "default": an interface we
// have never heard from is not a neutral state, it is one somebody needs to look at.
const toneFor = (s: string) =>
  ["CONNECTED", "CONTINUOUS", "IN_SYNC"].includes(s) ? "ok"
    : ["UNKNOWN", "RESYNC_REQUIRED", "RESYNCING"].includes(s) ? "warn"
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

// Connector kinds in the words a hotel uses. Unknown kinds fall through to the raw value rather than being
// hidden: an Interface created before the canonical set was narrowed still has to be identifiable.
const CONNECTOR_LABELS: Record<string, string> = {
  "protel-fias": "Protel (FIAS)",
};

export default function PMSInterfacesPage() {
  const [rows, setRows] = useState<PmsInterface[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [authoring, setAuthoring] = useState<string | null>(null);
  // The current configuration of the interface being edited, loaded before the form opens so it can start
  // from what is actually in use. null while it loads or when there is nothing published yet.
  const [authoringInitial, setAuthoringInitial] = useState<Record<string, unknown> | null>(null);
  const [note, setNote] = useState<string | null>(null);

  // openEditor loads the CURRENT published configuration and then opens the form on it. If nothing is
  // published there is nothing to start from, and the form opens on its defaults — which is the only case
  // where that is the right behaviour.
  async function openEditor(id: string) {
    setNote(null); setErr(null);
    try {
      const r = await api.get<{ revisions: PmsRevision[] }>(`/pms-interfaces/${id}/revisions`);
      const live = (r.revisions ?? []).find((x) => x.published);
      setAuthoringInitial(live ? { ...(live.config ?? {}), source_timezone: live.source_timezone } : null);
    } catch {
      setAuthoringInitial(null);
    }
    setAuthoring(id);
  }
  // The pending lifecycle change, held while the operator confirms. Both directions are consequential —
  // one opens a live connection to the property's PMS, the other stops every guest on every mapped network
  // being resolved — so neither happens on a single click.
  const [lifecycle, setLifecycle] = useState<{ id: string; to: string } | null>(null);

  const load = useCallback(async () => {
    setErr(null);
    try {
      const r = await api.get<{ interfaces: PmsInterface[] }>("/pms-interfaces");
      setRows(r.interfaces ?? []);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load PMS interfaces");
      setRows([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">PMS interfaces</h1>
        <Button onClick={() => { setCreating((v) => !v); setNote(null); }}>
          {creating ? "Cancel" : "Add interface"}
        </Button>
      </div>

      {note && <p className="text-sm text-emerald-700">{note}</p>}

      {lifecycle && (
        <LifecycleConfirm
          target={lifecycle}
          onCancel={() => setLifecycle(null)}
          onDone={async (msg) => { setLifecycle(null); setNote(msg); await load(); }}
          onError={(m) => setErr(m)}
        />
      )}

      {creating && (
        <Card>
          <CardBody>
            <CreateInterfaceForm
              onDone={(msg) => { setCreating(false); setNote(msg); void load(); }}
              onError={setErr}
            />
          </CardBody>
        </Card>
      )}

      {authoring && (
        <Card>
          <CardBody>
            <AuthorRevisionForm
              key={authoring}
              interfaceID={authoring}
              initial={authoringInitial}
              onDone={(msg) => { setAuthoring(null); setAuthoringInitial(null); setNote(msg); void load(); }}
              onError={setErr}
            />
          </CardBody>
        </Card>
      )}

      {err && (
        <p role="alert" className="text-sm text-red-600">
          {err}
        </p>
      )}

      <Card>
        <CardBody>
          {rows === null ? (
            <p className="text-sm">Loading…</p>
          ) : rows.length === 0 ? (
            <EmptyState
              title="No PMS interfaces"
              hint="This site has no PMS integration configured yet."
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Interface</TH>
                  <TH>PMS</TH>
                  <TH>Address</TH>
                  <TH>Currency</TH>
                  <TH>State</TH>
                  <TH>Configuration</TH>
                  <TH>&nbsp;</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((i) => (
                  <TR key={i.id}>
                    <TD>{i.display_label || "(unlabelled)"}</TD>
                    <TD>{CONNECTOR_LABELS[i.connector_kind] ?? i.connector_kind}</TD>
                    <TD className="text-xs">{i.endpoint || <span className="text-gray-500">—</span>}</TD>
                    <TD className="text-xs">{i.financial_base_currency || <span className="text-gray-500">—</span>}</TD>
                    <TD>
                      <Badge tone={toneFor(i.lifecycle_state) as any}>{words(LIFECYCLE_WORDS, i.lifecycle_state)}</Badge>
                    </TD>
                    <TD>
                      {/* The revision NUMBER is gone from this column. What an operator needs here is whether
                          the interface has a configuration in use at all; which numbered revision it happens
                          to be is audit detail and lives under History in the detail panel. */}
                      {i.published
                        ? <span className="text-xs text-gray-500">In use</span>
                        : <Badge tone="warn">not configured</Badge>}
                    </TD>
                    <TD className="whitespace-nowrap">
                      <Button
                        onClick={() => setSelected(selected === i.id ? null : i.id)}
                        aria-expanded={selected === i.id}
                      >
                        {selected === i.id ? "Close" : "Open"}
                      </Button>{" "}
                      {/*
                        A DECOMMISSIONED interface is in its terminal state: a revision authored against it
                        can never be published or dialled. Offering the button and refusing the save at the
                        backend would be correct but pointless -- the honest surface is not to offer it.
                      */}
                      {i.lifecycle_state !== "DECOMMISSIONED" && (
                        <Button variant="secondary" onClick={() => void openEditor(i.id)}>
                          Edit
                        </Button>
                      )}{" "}
                      {/*
                        PUTTING THE INTERFACE INTO USE. Creating and publishing were both reachable from this
                        screen and activation was not, so an operator could complete every visible step and
                        still have a connection that never dialled: the connector only picks up interfaces in
                        the ACTIVE state, and nothing in the product could produce it.

                        Activation is refused without a published revision — there would be no endpoint to
                        dial — so the button is offered only once one exists.
                      */}
                      {i.lifecycle_state === "AUTH_DISABLED" && i.published && (
                        <Button variant="secondary" onClick={() => setLifecycle({ id: i.id, to: "ACTIVE" })}>
                          Put into use
                        </Button>
                      )}
                      {i.lifecycle_state === "ACTIVE" && (
                        <Button variant="ghost" onClick={() => setLifecycle({ id: i.id, to: "AUTH_DISABLED" })}>
                          Take out of use
                        </Button>
                      )}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {selected && (
        <InterfaceDetail
          key={selected}
          id={selected}
          iface={rows?.find((r) => r.id === selected)}
          onChanged={load}
        />
      )}
    </div>
  );
}

function InterfaceDetail({
  id, iface, onChanged,
}: {
  id: string;
  iface?: PmsInterface;
  onChanged: () => void | Promise<void>;
}) {
  const [health, setHealth] = useState<PmsInterfaceHealth | null>(null);
  const [revisions, setRevisions] = useState<PmsRevision[] | null>(null);
  const [routes, setRoutes] = useState<PmsGuestNetworkRoute[]>([]);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    setErr(null);
    try {
      const [h, r, d] = await Promise.all([
        api.get<{ health: PmsInterfaceHealth }>(`/pms-interfaces/${id}/health`),
        api.get<{ revisions: PmsRevision[] }>(`/pms-interfaces/${id}/revisions`),
        api.get<{ guest_networks: PmsGuestNetworkRoute[] }>(`/pms-interfaces/${id}`),
      ]);
      setHealth(h.health);
      setRevisions(r.revisions ?? []);
      setRoutes(d.guest_networks ?? []);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load the interface");
      setRevisions([]);
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  const reload = async () => {
    await load();
    await onChanged();
  };

  return (
    <div className="space-y-4">
      {err && (
        <p role="alert" className="text-sm text-red-600">
          {err}
        </p>
      )}

      <HealthCard health={health} />
      <SynchronizationCard id={id} health={health} onRefreshed={load} />
      <RevisionsCard id={id} iface={iface} revisions={revisions} onPublished={reload} />
      {/* The Credential card is not rendered. The supported connector's link carries no transport
          authentication (credential_mode=NONE), so there is no credential to set or rotate — and the card's
          "never set" warning described a missing secret that is not supposed to exist, which read as a
          misconfiguration on a correctly configured interface. The component and its endpoint are left in
          place for a future connector that genuinely authenticates. */}
      <RoutingCard routes={routes} />
    </div>
  );
}

function HealthCard({ health }: { health: PmsInterfaceHealth | null }) {
  if (!health) return null;
  // The four dimensions are shown side by side rather than collapsed into one word. "Degraded" tells an
  // operator nothing about what to do; "connected but out of sync" tells them to look at the resync.
  // Each label is the QUESTION the axis answers, and each value is in operator words. "Transport /
  // continuity / synchronization" named the internal axes; an operator wants to know whether the PMS is
  // reachable, whether updates are still arriving, and whether the appliance's copy is complete.
  const dims: [string, string, string, string | null | undefined][] = [
    ["Connection", words(TRANSPORT_WORDS, health.transport_status), health.transport_status, health.transport_error_code || null],
    ["Live updates", words(CONTINUITY_WORDS, health.continuity_status), health.continuity_status, null],
    ["Guest list", words(SYNC_WORDS, health.sync_status), health.sync_status, health.last_sync_failure_code || null],
  ];
  return (
    <Card>
      <CardBody>
        <h2 className="text-lg font-medium">Connection status</h2>
        <dl className="mt-2 grid grid-cols-1 gap-3 sm:grid-cols-3">
          {dims.map(([label, text, raw, detail]) => (
            <div key={label}>
              <dt className="text-xs uppercase text-gray-500">{label}</dt>
              <dd>
                <Badge tone={toneFor(raw) as any}>{text}</Badge>
                {/* The raw code stays available as a tooltip: it is what appears in the daemon log, so an
                    operator reading it to support can still quote the exact value. */}
                {detail && <span className="ml-2 text-xs text-gray-600" title={raw}>{detail}</span>}
              </dd>
            </div>
          ))}
        </dl>

        <h3 className="mt-4 text-sm font-medium">Occupancy</h3>
        <p className="text-sm">
          {health.in_house_stays} stay{health.in_house_stays === 1 ? "" : "s"} in house
          {health.last_stay_event_at && <> · last event {formatRelative(health.last_stay_event_at)}</>}
        </p>

        <h3 className="mt-4 text-sm font-medium">Ingestion backlog</h3>
        <p className="text-sm">
          {health.pending_events} waiting · {health.review_events} needing review
          {/* The age of the oldest waiting event is the number that distinguishes a busy morning from a
              stuck processor, so it is stated rather than left to be inferred from the count. */}
          {health.oldest_pending_at && <> · oldest waiting since {formatRelative(health.oldest_pending_at)}</>}
        </p>
      </CardBody>
    </Card>
  );
}

function RevisionsCard({
  id, iface, revisions, onPublished,
}: {
  id: string;
  iface?: PmsInterface;
  revisions: PmsRevision[] | null;
  onPublished: () => void | Promise<void>;
}) {
  const [publishing, setPublishing] = useState<string | null>(null);
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function publish(revisionID: string) {
    setBusy(true);
    setErr(null);
    try {
      await api.post(`/pms-interfaces/${id}/publish`, {
        revision_id: revisionID,
        // The Revision this operator believed was live. If it changed while the form was open, edged refuses
        // rather than reverting whoever published in between.
        expected_revision_id: iface?.current_revision_id ?? "",
        reason_code: reason.trim(),
        password,
      });
      setPublishing(null);
      setReason("");
      setPassword("");
      await onPublished();
    } catch (e: any) {
      setErr(e?.message ?? "The publication was refused");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardBody>
        <h2 className="text-lg font-medium">History</h2>
        <p className="mt-1 text-sm text-gray-600">
          Every saved configuration is kept permanently and is read-only here. Changing settings records a new one, so every
          stay records exactly what the interface was configured as when it was resolved.
        </p>

        {err && (
          <p role="alert" className="mt-2 text-sm text-red-600">
            {err}
          </p>
        )}

        {revisions === null ? (
          <p className="mt-3 text-sm">Loading…</p>
        ) : revisions.length === 0 ? (
          <EmptyState title="No revisions" hint="This interface has no configuration revisions yet." />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Revision</TH>
                <TH>Time zone</TH>
                <TH>Folio identity</TH>
                <TH>Configuration</TH>
                <TH>&nbsp;</TH>
              </TR>
            </THead>
            <tbody>
              {revisions.map((r) => (
                <TR key={r.id}>
                  <TD>
                    #{r.revision_no}{" "}
                    {r.published && <Badge tone="ok">published</Badge>}
                  </TD>
                  <TD>{r.source_timezone}</TD>
                  <TD>{pretty(r.folio_identity_strategy)}</TD>
                  <TD>
                    {/* The raw config object used to be dumped here as JSON. It is redacted server-side, so
                        nothing secret was exposed — but "readable" is not the same as "already safe", and a
                        wall of snake_case keys and millisecond integers is not how an operator checks that a
                        PMS is configured correctly. The same values, in words. */}
                    <RevisionSummary config={r.config} />
                  </TD>
                  <TD>
                    {!r.published && (
                      <Button onClick={() => setPublishing(r.id)} aria-expanded={publishing === r.id}>
                        Publish
                      </Button>
                    )}
                  </TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}

        {publishing && (
          <form
            className="mt-4 space-y-2 border-t pt-3"
            onSubmit={(e) => {
              e.preventDefault();
              void publish(publishing);
            }}
          >
            <p className="text-sm">
              Publishing changes what every guest is resolved against from this moment on.
            </p>
            <label className="block text-sm">
              Reason
              <input
                className="mt-1 block w-full rounded border px-2 py-1"
                value={reason}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setReason(e.target.value)}
                placeholder="CONFIG_UPDATE"
                required
              />
            </label>
            <label className="block text-sm">
              Confirm your password
              <input
                type="password"
                autoComplete="current-password"
                className="mt-1 block w-full rounded border px-2 py-1"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </label>
            <div className="flex gap-2">
              <Button type="submit" disabled={busy}>
                {busy ? "Publishing…" : "Publish revision"}
              </Button>
              <Button type="button" onClick={() => setPublishing(null)}>
                Cancel
              </Button>
            </div>
          </form>
        )}
      </CardBody>
    </Card>
  );
}

function CredentialCard({
  id, iface, onRotated,
}: {
  id: string;
  iface?: PmsInterface;
  onRotated: () => void | Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [secret, setSecret] = useState("");
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  // The pending lifecycle change, held while the operator confirms. Both directions are consequential —
  // one opens a live connection to the property's PMS, the other stops every guest on every mapped network
  // being resolved — so neither happens on a single click.
  const [lifecycle, setLifecycle] = useState<{ id: string; to: string } | null>(null);
  const [busy, setBusy] = useState(false);

  async function rotate(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    setNote(null);
    try {
      const r = await api.post<{ generation_no: number }>(`/pms-interfaces/${id}/secret`, {
        secret,
        reason_code: reason.trim(),
        password,
      });
      // The confirmation names the generation, never the value — there is nowhere to read the value back
      // from, and echoing it here would create the one place it could be read.
      setNote(`Credential generation ${r.generation_no} is now in use.`);
      setSecret("");
      setReason("");
      setPassword("");
      setOpen(false);
      await onRotated();
    } catch (e: any) {
      setErr(e?.message ?? "The rotation was refused");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardBody>
        <h2 className="text-lg font-medium">Credential</h2>
        <p className="mt-1 text-sm text-gray-600">
          The credential can be set and replaced. It cannot be read back — not here, and not by anyone.
        </p>
        <p className="mt-2 text-sm">
          {iface?.secret_generation
            ? <>Currently using generation {iface.secret_generation}
              {iface.secret_rotated_at && <> · last replaced {formatRelative(iface.secret_rotated_at)}</>}</>
            : "No credential has been set for this interface."}
        </p>

        {note && (
          <p role="status" className="mt-2 text-sm text-green-700">
            {note}
          </p>
        )}
        {err && (
          <p role="alert" className="mt-2 text-sm text-red-600">
            {err}
          </p>
        )}

        {!open ? (
          <Button className="mt-3" onClick={() => setOpen(true)} aria-expanded={false}>
            Replace credential
          </Button>
        ) : (
          <form className="mt-3 space-y-2 border-t pt-3" onSubmit={rotate}>
            <label className="block text-sm">
              New credential
              <input
                type="password"
                autoComplete="off"
                className="mt-1 block w-full rounded border px-2 py-1"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                required
              />
            </label>
            <label className="block text-sm">
              Reason
              <input
                className="mt-1 block w-full rounded border px-2 py-1"
                value={reason}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setReason(e.target.value)}
                placeholder="ROTATION"
                required
              />
            </label>
            <label className="block text-sm">
              Confirm your password
              <input
                type="password"
                autoComplete="current-password"
                className="mt-1 block w-full rounded border px-2 py-1"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </label>
            <div className="flex gap-2">
              <Button type="submit" disabled={busy}>
                {busy ? "Replacing…" : "Replace credential"}
              </Button>
              <Button type="button" onClick={() => setOpen(false)}>
                Cancel
              </Button>
            </div>
          </form>
        )}
      </CardBody>
    </Card>
  );
}

function RoutingCard({ routes }: { routes: PmsGuestNetworkRoute[] }) {
  return (
    <Card>
      <CardBody>
        <h2 className="text-lg font-medium">Guest networks</h2>
        {routes.length === 0 ? (
          // An interface no network routes to is configured but unreachable by any guest — worth saying,
          // because it looks identical to a healthy interface everywhere else on this page.
          <p className="mt-1 text-sm">
            No guest network routes to this interface, so no guest is resolved against it.
          </p>
        ) : (
          <ul className="mt-1 text-sm">
            {routes.map((r) => (
              <li key={r.guest_network_id}>
                {r.guest_network_name || r.guest_network_id} · {pretty(r.routing_mode)}
                {r.is_default && (
                  <>
                    {" · "}
                    <Badge tone="info">default</Badge>
                  </>
                )}
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
function CreateInterfaceForm({ onDone, onError }: {
  onDone: (msg: string) => void; onError: (e: string | null) => void;
}) {
  // ONE SUPPORTED CONNECTOR, AND THE SCREEN SAYS SO.
  //
  // The dropdown offered six kinds, carried over from the legacy scd provider list. pmsd supports one and
  // refuses the rest, so choosing "mews" produced an Interface that could be created, configured and
  // published — and then rejected by the connector, surfacing at the last step as a connection failure
  // rather than as a connector this build does not implement. edged now refuses the other kinds outright;
  // this is the screen matching that, not the only line of defence.
  const kind = "protel-fias";
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <form
      className="space-y-3"
      onSubmit={async (e) => {
        e.preventDefault(); setBusy(true); onError(null);
        try {
          await api.post("/pms-interfaces", { connector_kind: kind, display_label: label.trim() });
          onDone("Interface created. It starts with authentication disabled and nothing published — configure a revision, then publish it.");
        } catch (err: any) { onError(err?.message ?? "Could not create the interface"); }
        finally { setBusy(false); }
      }}
    >
      <h2 className="font-medium">New PMS interface</h2>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="block text-sm">
          Connector
          <div className="mt-1 rounded border border-border bg-panel2 px-2 py-1.5">Protel (FIAS)</div>
          <span className="block text-xs text-gray-500">
            The only property management system this appliance can connect to today.
          </span>
        </div>
        <label className="block text-sm">
          Name
          <input className="mt-1 block w-full rounded border px-2 py-1" value={label} required maxLength={120}
                 placeholder="Front office Protel" onChange={(e) => setLabel(e.target.value)} />
        </label>
      </div>
      <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create interface"}</Button>
    </form>
  );
}

function AuthorRevisionForm({ interfaceID, initial, onDone, onError }: {
  interfaceID: string;
  // The CURRENT configuration, so Edit means "change what is there" rather than "retype it".
  //
  // IT USED TO START BLANK. Every field was a hardcoded default, so an operator changing a single timeout
  // silently reset the endpoint, the time zone and every other value to whatever this file happened to
  // default to — and the form looked exactly the same either way. That is not an edit; it is a new
  // configuration wearing an edit's clothes.
  initial?: Record<string, unknown> | null;
  onDone: (msg: string) => void; onError: (e: string | null) => void;
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
  const num = (k: string) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setF({ ...f, [k]: Number(e.target.value) });
  const numField = (k: string, label: string, hint?: string) => (
    <label className="block text-sm">
      {label}
      <input type="number" min={1} className="mt-1 block w-full rounded border px-2 py-1"
             value={String((f as any)[k])} onChange={num(k)} required />
      {hint && <span className="block text-xs text-gray-500">{hint}</span>}
    </label>
  );
  return (
    <form
      className="space-y-3"
      onSubmit={async (e) => {
        e.preventDefault(); setBusy(true); onError(null);
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
          onDone("Draft revision saved. It is not live until you publish it.");
        } catch (err: any) { onError(err?.message ?? "Could not save the revision"); }
        finally { setBusy(false); }
      }}
    >
      <h2 className="font-medium">{initial ? "Edit configuration" : "Configure this interface"}</h2>
      <p className="text-xs text-gray-500">
        {initial
          ? "These are the settings in use now. Saving records the change; it does not take effect until you put it live, which is a separate confirmed action."
          : "Saved as a draft. Putting it live is a separate, confirmed action."}{" "}
        This connection is read-only.
      </p>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block text-sm">
          PMS address and port
          <input className="mt-1 block w-full rounded border px-2 py-1" required placeholder="pms.hotel.local:5010"
                 value={f.endpoint} onChange={(e) => setF({ ...f, endpoint: e.target.value })} />
          <span className="block text-xs text-gray-500">host:port the connector dials</span>
        </label>
        <label className="block text-sm">
          PMS time zone
          <input className="mt-1 block w-full rounded border px-2 py-1" required placeholder="Africa/Cairo"
                 value={f.source_timezone} onChange={(e) => setF({ ...f, source_timezone: e.target.value })} />
          <span className="block text-xs text-gray-500">IANA name; arrivals and departures are read in it</span>
        </label>
        {/* Fixed facts of the supported connector, shown so the operator can SEE them rather than set
            them. An operator still needs to know the link is read-only and unauthenticated; what they do not
            need is a control that can only be set one way. */}
        <div className="rounded border border-border bg-panel2 px-3 py-2 text-xs text-gray-500 sm:col-span-2">
          <div className="font-medium text-text mb-1">Fixed for this connector</div>
          Read-only — StayConnect never writes to the PMS · No credential required — the link is
          unauthenticated · Full guest-list refresh supported · Folio identity not set, so charging a room
          through the PMS stays disabled until it is determined from the property&rsquo;s own folio behaviour.
        </div>
        {numField("dial_timeout_ms", "Dial timeout (ms)")}
        {numField("read_timeout_ms", "Read timeout (ms)")}
        {numField("write_timeout_ms", "Write timeout (ms)")}
        {numField("heartbeat_interval_ms", "Heartbeat every (ms)")}
        {numField("heartbeat_timeout_ms", "Heartbeat timeout (ms)", "must exceed the interval")}
        {numField("feed_freshness_ms", "Feed considered stale after (ms)")}
        {numField("complete_sync_ms", "Complete sync bound (ms)")}
        <label className="block text-sm">
          Financial base currency (optional)
          <input className="mt-1 block w-full rounded border px-2 py-1" maxLength={3} placeholder="USD"
                 value={f.financial_base_currency}
                 onChange={(e) => setF({ ...f, financial_base_currency: e.target.value.toUpperCase() })} />
        </label>
        <label className="block text-sm">
          Currency exponent (optional)
          <input type="number" min={0} max={4} className="mt-1 block w-full rounded border px-2 py-1"
                 value={f.financial_base_currency_exponent}
                 onChange={(e) => setF({ ...f, financial_base_currency_exponent: e.target.value })} />
          <span className="block text-xs text-gray-500">set together with the currency, or leave both empty</span>
        </label>
      </div>
      <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Save draft revision"}</Button>
    </form>
  );
}


// LifecycleConfirm asks for a reason and a password before an interface is put into or taken out of use.
//
// Both directions change what happens to real guests — one opens a live connection to the property's PMS,
// the other stops every guest on every mapped network from being resolved — so edged requires a bounded
// reason code and a password step-up on this route. Collecting them here means the refusal an operator sees
// is "you did not confirm", not an unexplained 401 after the click appeared to work.
function LifecycleConfirm({
  target, onCancel, onDone, onError,
}: {
  target: { id: string; to: string };
  onCancel: () => void;
  onDone: (msg: string) => void | Promise<void>;
  onError: (m: string) => void;
}) {
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const activating = target.to === "ACTIVE";

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api.post(`/pms-interfaces/${target.id}/lifecycle`, {
        state: target.to, reason_code: reason.trim(), password,
      });
      await onDone(activating
        ? "This PMS connection is now in use. It may take a moment to connect and load the guest list."
        : "This PMS connection is no longer in use. Guests on networks routed to it can no longer sign in with their room details.");
    } catch (e: any) {
      onError(e?.message ?? "Could not change the connection state");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardBody>
        <h2 className="text-base font-semibold">
          {activating ? "Put this PMS connection into use?" : "Take this PMS connection out of use?"}
        </h2>
        <p className="text-sm text-gray-600 mt-1">
          {activating
            ? "The appliance will connect to the property management system and load the current guest list."
            : "Guests on networks routed to this connection will no longer be able to sign in with their room number and name. Stays already recorded are kept."}
        </p>
        <form onSubmit={submit} className="grid gap-3 sm:grid-cols-2 mt-3">
          <div>
            <Label>Reason</Label>
            <Input value={reason} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setReason(e.target.value)} required
              placeholder={activating ? "Commissioning" : "Maintenance"} />
          </div>
          <div>
            <Label>Confirm your password</Label>
            <Input type="password" value={password} onChange={(e: React.ChangeEvent<HTMLInputElement>) => setPassword(e.target.value)} required />
          </div>
          <div className="sm:col-span-2 flex gap-2">
            <Button type="submit" disabled={busy || !reason.trim() || !password}>
              {busy ? "Working…" : activating ? "Put into use" : "Take out of use"}
            </Button>
            <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
          </div>
        </form>
      </CardBody>
    </Card>
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
    <div className="text-xs space-y-0.5 max-w-md">
      {rows.map(([k, v]) => (
        <div key={k}><span className="text-gray-500">{k}:</span> {v}</div>
      ))}
      {extra.map((k) => (
        <div key={k}><span className="text-gray-500">{k}:</span> {String(config[k])}</div>
      ))}
    </div>
  );
}
