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
  const [creating, setCreating] = useState(false);
  const [authoring, setAuthoring] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);

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
              interfaceID={authoring}
              onDone={(msg) => { setAuthoring(null); setNote(msg); void load(); }}
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
                        <Button variant="secondary" onClick={() => { setAuthoring(i.id); setNote(null); }}>
                          Configure
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
  const [kind, setKind] = useState("protel-fias");
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
        <label className="block text-sm">
          Connector
          <select className="mt-1 block w-full rounded border px-2 py-1" value={kind}
                  onChange={(e) => setKind(e.target.value)}>
            {["protel-fias", "opera-fias", "fidelio-fias", "mews", "apaleo", "stub"].map((k) => (
              <option key={k} value={k}>{k}</option>
            ))}
          </select>
        </label>
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

function AuthorRevisionForm({ interfaceID, onDone, onError }: {
  interfaceID: string; onDone: (msg: string) => void; onError: (e: string | null) => void;
}) {
  const [f, setF] = useState({
    endpoint: "", source_timezone: "Africa/Cairo", folio_identity_strategy: "GLOBALLY_UNIQUE",
    normalization_version: 1, credential_mode: "AUTH_KEY", resync_supported: true,
    dial_timeout_ms: 5000, read_timeout_ms: 15000, write_timeout_ms: 15000,
    heartbeat_interval_ms: 30000, heartbeat_timeout_ms: 90000,
    feed_freshness_ms: 120000, complete_sync_ms: 600000,
    financial_base_currency: "", financial_base_currency_exponent: "",
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
      <h2 className="font-medium">Configure a revision</h2>
      <p className="text-xs text-gray-500">
        Saved as a draft. Publishing it is a separate, confirmed action, and this connection is read-only.
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
        <label className="block text-sm">
          Folio identity
          <select className="mt-1 block w-full rounded border px-2 py-1" value={f.folio_identity_strategy}
                  onChange={(e) => setF({ ...f, folio_identity_strategy: e.target.value })}>
            {["UNSET", "GLOBALLY_UNIQUE", "UNIQUE_PER_STAY", "REUSED_SEQUENTIAL"].map((v) => (
              <option key={v} value={v}>{v}</option>
            ))}
          </select>
        </label>
        <label className="block text-sm">
          Credential mode
          <select className="mt-1 block w-full rounded border px-2 py-1" value={f.credential_mode}
                  onChange={(e) => setF({ ...f, credential_mode: e.target.value })}>
            <option value="AUTH_KEY">AUTH_KEY — a credential is required</option>
            <option value="NONE">NONE — the PMS link is unauthenticated</option>
          </select>
        </label>
        {numField("normalization_version", "Normalization version")}
        <label className="block text-sm">
          Resync supported
          <select className="mt-1 block w-full rounded border px-2 py-1" value={String(f.resync_supported)}
                  onChange={(e) => setF({ ...f, resync_supported: e.target.value === "true" })}>
            <option value="true">yes</option>
            <option value="false">no</option>
          </select>
        </label>
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
