"use client";

// THE SYNCHRONIZATION SECTION — what is happening with the guest list, in words a duty manager can act on.
//
// The hard rule here is about what is NOT displayed. Protel's FIAS gives no record total before the end of a
// sync: there is no field carrying it and no way to derive it. So there is no progress bar, no percentage and
// no "X of Y" anywhere below, because every one of those would be a number invented by this file. What there
// is instead is the real count of records received so far and a sentence saying plainly why that is all we
// can offer. An operator who reads "1,847 records received" learns something true; one who reads "63%" has
// been told a number the PMS never sent, and will believe it.
//
// The polling exists for the same reason the section does. A sync takes as long as the hotel's roster takes,
// and an operator who has to keep pressing refresh cannot tell a slow sync from a stalled one.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type PmsInterfaceHealth } from "@/lib/api";

// Stages, in the order they occur. The server sends the token; the wording lives here so it can be phrased for
// hotel staff rather than for engineers, and an unrecognised token falls back rather than rendering raw.
const STAGE_WORDS: Record<string, string> = {
  REQUESTING_FULL_SYNC: "Requesting a full sync",
  WAITING_FOR_PMS: "Waiting for the PMS to start sending",
  RECEIVING: "Receiving the guest list",
  PUBLISHING: "Publishing the new guest list",
  COMPLETE: "Complete",
  APPLYING: "Applying guest list",
  FAILED: "Failed",
  INTERRUPTED: "Interrupted",
};

// While a sync is in one of these stages there is more to come, so the page keeps polling.
const ACTIVE_STAGES = new Set(["REQUESTING_FULL_SYNC", "WAITING_FOR_PMS", "RECEIVING", "PUBLISHING", "APPLYING"]);

// THE EFFECTIVE STAGE, from two durable facts the server reports separately.
//
// sync_stage=COMPLETE means the generation was PUBLISHED. It does NOT mean the guest list is usable: the
// applier writes it into the Stay tables afterwards, and Room sign-in is correctly closed until it finishes.
// Showing "Complete" during that gap told the operator the sync was done while guests were still being
// refused, so the card derives an APPLYING stage instead. The durable COMPLETE token is not redefined —
// nothing here writes it, and the server still reports it verbatim.
function effectiveStage(stage: string, ready: boolean | undefined): string {
  if (stage === "COMPLETE" && ready === false) return "APPLYING";
  return stage;
}

const REASONS: { value: string; label: string }[] = [
  { value: "SUSPECTED_STALE_GUEST_LIST", label: "The guest list looks out of date" },
  { value: "AFTER_PMS_MAINTENANCE", label: "After PMS maintenance" },
  { value: "OPERATOR_VERIFICATION", label: "Checking the connection works" },
  { value: "SUPPORT_REQUEST", label: "Asked to by support" },
];

const when = (t?: string | null) => (t ? new Date(t).toLocaleString() : "—");

export function SynchronizationCard({
  id,
  health,
  onRefreshed,
}: {
  id: string;
  health: PmsInterfaceHealth | null;
  onRefreshed: () => void | Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [reason, setReason] = useState(REASONS[0].value);
  const [password, setPassword] = useState("");

  const stage = effectiveStage(health?.sync_stage ?? "", health?.materialization_ready);
  const active = ACTIVE_STAGES.has(stage);

  // ONE POLL LOOP, TWO RATES.
  //
  // Polling only while a sync was already active was not enough, and the gap mattered: the operator restores
  // the PMS socket by hand with this page open, and everything interesting — the connection coming up, the
  // automatic full sync starting — happens BEFORE any stage is active. A page that only wakes up once
  // something is active can never observe the thing that starts it.
  //
  // So the card polls whenever it is mounted: slowly while nothing is happening, quickly once a sync is
  // running. It is deliberately ONE interval whose delay changes, not two effects that could both be alive:
  // the effect depends only on `delay`, so changing rate replaces the timer rather than adding one.
  //
  // onRefreshed is held in a ref for the same reason. In the dependency array it would tear down and rebuild
  // the timer on every parent render — and this page re-renders on every poll result, so the interval would
  // reset just before it was due and effectively never fire.
  const refreshRef = useRef(onRefreshed);
  refreshRef.current = onRefreshed;
  const delay = active ? 3000 : 10000;
  useEffect(() => {
    const t = setInterval(() => void refreshRef.current(), delay);
    return () => clearInterval(t);
  }, [delay]);

  const canRequest =
    health?.transport_status === "CONNECTED" && !active && health?.sync_status !== "RESYNC_IN_PROGRESS";

  const request = useCallback(async () => {
    setBusy(true);
    setErr(null);
    setNote(null);
    try {
      const r = await api.post<{ note?: string }>(`/pms-interfaces/${id}/full-resync`, {
        reason_code: reason,
        password,
      });
      setPassword("");
      setNote(r?.note ?? "The request is recorded.");
      await refreshRef.current();
    } catch (e) {
      // The server names the precondition that stopped it — "the PMS is not connected" rather than a bare
      // refusal — because a button that looks like it should work and silently does nothing is how an
      // operator concludes the product is broken.
      setErr(e instanceof ApiError ? e.message : (e as Error)?.message ?? "The request could not be recorded");
    } finally {
      setBusy(false);
    }
  }, [id, reason, password]);

  return (
    <section className="rounded-lg border p-4 space-y-4">
      <div className="flex items-start justify-between gap-4 flex-wrap">
        <div>
          <h2 className="font-semibold">Synchronization</h2>
          <p className="text-sm text-muted mt-1 max-w-2xl">
            The first successful connection to the property management system automatically loads the full
            guest list. <strong>Full Resync Now</strong> asks for another complete, fresh copy afterwards —
            use it if the list here looks out of date.
          </p>
        </div>
        <span className="text-xs rounded-full border px-2 py-1" aria-live="polite">
          {active ? "Updating automatically…" : "Watching for changes"}
        </span>
      </div>

      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        {/* Connection and sync state are NOT repeated here: the Health card above already carries them, and
            two copies of the same fact on one page eventually disagree. This section owns the sync itself. */}
        <Row label="Sync stage" value={stage ? STAGE_WORDS[stage] ?? "In progress" : "—"} />
        <Row label="Requested" value={when(health?.resync_command_requested_at)} />
        <Row label="PMS started sending" value={when(health?.resync_started_at)} />
        <Row label="Last completed full sync" value={when(health?.last_complete_sync_at)} />
        <Row label="Records received" value={(health?.sync_records_received ?? 0).toLocaleString()} />
        <Row
          label="Records skipped"
          value={
            health?.sync_records_skipped
              ? `${health.sync_records_skipped.toLocaleString()} (no guest identity)`
              : "0"
          }
        />
        {/* The ordinary live figure, and only that. The old last_sync_in_house_count was stamped at the
            publish barrier and reported the roster the sync replaced — 461 beside a live 595. Once the stage
            above means materialized, this number is correct by the time it says Complete. */}
        <Row label="Guests in house" value={(health?.in_house_stays ?? 0).toLocaleString()} />
        {health?.sync_failure_code && <Row label="Reason it stopped" value={health.sync_failure_code} />}
      </dl>

      {/* THE HONEST SENTENCE. Shown only while receiving, because that is the only stage where an operator
          would otherwise expect a total and wonder why there isn't one. */}
      {stage === "RECEIVING" && (
        <p className="text-sm rounded-md border bg-muted/30 p-3">
          Receiving records — the PMS does not provide a total, so there is no percentage to show. Waiting for
          the end-of-sync signal. <strong>{(health?.sync_records_received ?? 0).toLocaleString()}</strong>{" "}
          records received so far.
        </p>
      )}
      {(stage === "REQUESTING_FULL_SYNC" || stage === "WAITING_FOR_PMS") && (
        <p className="text-sm rounded-md border bg-muted/30 p-3">
          A full sync has been requested. This happens automatically the first time the connection to the
          property management system succeeds — you do not need to do anything.
        </p>
      )}
      {stage === "APPLYING" && (
        <p className="text-sm rounded-md border bg-muted/30 p-3">
          The guest list has arrived and is being applied. Room sign-in resumes automatically the moment it
          finishes — usually a few seconds.
        </p>
      )}
      {stage === "INTERRUPTED" && (
        <p className="text-sm rounded-md border p-3">
          The sync did not finish, so the previous guest list is still in use — nothing was lost or partly
          replaced. You can request another one.
        </p>
      )}

      <div className="flex flex-wrap items-end gap-3 border-t pt-4">
        <label className="text-sm">
          <span className="block mb-1">Why resynchronize</span>
          <select
            className="border rounded-md px-2 py-1"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            disabled={!canRequest || busy}
          >
            {REASONS.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
        </label>
        <label className="text-sm">
          <span className="block mb-1">Password to confirm this resync</span>
          <input
            type="password"
            className="border rounded-md px-2 py-1"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={!canRequest || busy}
            autoComplete="current-password"
          />
        </label>
        <button
          type="button"
          className="rounded-md border px-3 py-1.5 text-sm font-medium disabled:opacity-50"
          onClick={() => void request()}
          disabled={!canRequest || busy || password === ""}
        >
          {busy ? "Requesting…" : "Full Resync Now"}
        </button>
        {!canRequest && (
          <span className="text-sm text-muted">
            {health?.transport_status !== "CONNECTED"
              ? "Available when the PMS is connected."
              : "A synchronization is already running."}
          </span>
        )}
      </div>

      {note && <p className="text-sm">{note}</p>}
      {err && (
        <p role="alert" className="text-sm text-red-600">
          {err}
        </p>
      )}
    </section>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4 border-b py-1 last:border-0">
      <dt className="text-muted">{label}</dt>
      <dd className="font-medium text-right">{value}</dd>
    </div>
  );
}
