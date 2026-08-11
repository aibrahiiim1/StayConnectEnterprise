"use client";

// Phase 3 (DARK) — PMS INTERFACES.
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
import {
  api, PmsInterface, PmsInterfaceHealth, PmsRevision, PmsGuestNetworkRoute,
} from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative } from "@/lib/utils";

// toneFor maps a status word to a colour. UNKNOWN is deliberately "warn" and not "default": an interface we
// have never heard from is not a neutral state, it is one somebody needs to look at.
const toneFor = (s: string) =>
  ["CONNECTED", "CONTINUOUS", "IN_SYNC"].includes(s) ? "ok"
    : ["UNKNOWN", "RESYNC_REQUIRED", "RESYNCING"].includes(s) ? "warn"
      : ["DISCONNECTED", "GAP_DETECTED", "OUT_OF_SYNC"].includes(s) ? "err" : "default";

const pretty = (s: string) => s.replace(/_/g, " ").toLowerCase();

export default function PMSInterfacesPage() {
  const [rows, setRows] = useState<PmsInterface[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);

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
      <h1 className="text-xl font-semibold">PMS interfaces</h1>

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
                  <TH>Connector</TH>
                  <TH>State</TH>
                  <TH>Published revision</TH>
                  <TH>Credential</TH>
                  <TH>&nbsp;</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((i) => (
                  <TR key={i.id}>
                    <TD>{i.display_label || "(unlabelled)"}</TD>
                    <TD>{i.connector_kind}</TD>
                    <TD>
                      <Badge tone={toneFor(i.lifecycle_state) as any}>{pretty(i.lifecycle_state)}</Badge>
                    </TD>
                    <TD>
                      {i.published ? (
                        <>
                          #{i.current_revision_no ?? "?"}{" "}
                          <span className="text-xs text-gray-500">of {i.revision_count}</span>
                        </>
                      ) : (
                        // An interface with nothing published resolves nothing at all. Saying so plainly
                        // beats an empty cell that reads as "not loaded yet".
                        <Badge tone="warn">nothing published</Badge>
                      )}
                    </TD>
                    <TD>
                      {i.secret_generation ? (
                        <>generation {i.secret_generation}</>
                      ) : (
                        <Badge tone="warn">never set</Badge>
                      )}
                    </TD>
                    <TD>
                      <Button
                        onClick={() => setSelected(selected === i.id ? null : i.id)}
                        aria-expanded={selected === i.id}
                      >
                        {selected === i.id ? "Close" : "Open"}
                      </Button>
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
      <RevisionsCard id={id} iface={iface} revisions={revisions} onPublished={reload} />
      <CredentialCard id={id} iface={iface} onRotated={reload} />
      <RoutingCard routes={routes} />
    </div>
  );
}

function HealthCard({ health }: { health: PmsInterfaceHealth | null }) {
  if (!health) return null;
  // The four dimensions are shown side by side rather than collapsed into one word. "Degraded" tells an
  // operator nothing about what to do; "connected but out of sync" tells them to look at the resync.
  const dims: [string, string, string | null | undefined][] = [
    ["Transport", health.transport_status, health.transport_error_code || null],
    ["Continuity", health.continuity_status, null],
    ["Synchronization", health.sync_status, health.last_sync_failure_code || null],
  ];
  return (
    <Card>
      <CardBody>
        <h2 className="text-lg font-medium">Health</h2>
        <dl className="mt-2 grid grid-cols-1 gap-3 sm:grid-cols-3">
          {dims.map(([label, value, detail]) => (
            <div key={label}>
              <dt className="text-xs uppercase text-gray-500">{label}</dt>
              <dd>
                <Badge tone={toneFor(value) as any}>{pretty(value)}</Badge>
                {detail && <span className="ml-2 text-xs text-gray-600">{detail}</span>}
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
        <h2 className="text-lg font-medium">Revisions</h2>
        <p className="mt-1 text-sm text-gray-600">
          A revision is never edited. Changing configuration means publishing a different revision, so every
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
                    {/* Already redacted server-side; rendered as-is so nothing here can un-redact it. */}
                    <pre className="max-w-md overflow-x-auto text-xs">{JSON.stringify(r.config, null, 1)}</pre>
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
                onChange={(e) => setReason(e.target.value)}
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
                onChange={(e) => setReason(e.target.value)}
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
