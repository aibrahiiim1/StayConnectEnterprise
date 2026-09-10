"use client";

// THE GUEST-LIST REFRESH — what is happening with the guest list, in words a duty manager can act on.
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
//
// WHAT CHANGED: the reason and the password used to be two inline fields sitting beside the button, so a
// read-only status page asked for a password with nothing saying what it was for, and the reason dropdown
// looked like a filter on the table above it. Both now live in a confirmation dialog that states what the
// action does — and critically, that the current guest list stays in use until the new one is complete, which
// is the thing an operator hesitates over before pressing it.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, type PmsInterfaceHealth } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Select, Field } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Separator } from "@/components/ui/misc";
import { ConfirmDialog } from "@/components/ui/dialog";
import { RefreshCw } from "lucide-react";

// Stages, in the order they occur. The server sends the token; the wording lives here so it can be phrased for
// hotel staff rather than for engineers, and an unrecognised token falls back rather than rendering raw.
const STAGE_WORDS: Record<string, string> = {
  REQUESTING_FULL_SYNC: "Requesting the full list",
  WAITING_FOR_PMS: "Waiting for the PMS to start sending",
  RECEIVING: "Receiving the guest list",
  PUBLISHING: "Publishing the new guest list",
  COMPLETE: "Complete",
  APPLYING: "Applying the guest list",
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
  const [err, setErr] = useState<unknown>(null);
  const [note, setNote] = useState<string | null>(null);
  const [reason, setReason] = useState(REASONS[0].value);
  const [confirming, setConfirming] = useState(false);

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

  const request = useCallback(
    async (password: string) => {
      setBusy(true);
      setErr(null);
      setNote(null);
      try {
        const r = await api.post<{ note?: string }>(`/pms-interfaces/${id}/full-resync`, {
          reason_code: reason,
          password,
        });
        setConfirming(false);
        setNote(r?.note ?? "The refresh has been requested. Its progress appears above as the PMS responds.");
        await refreshRef.current();
      } catch (e) {
        // The server names the precondition that stopped it — "the PMS is not connected" rather than a bare
        // refusal — because a button that looks like it should work and silently does nothing is how an
        // operator concludes the product is broken.
        setErr(e);
      } finally {
        setBusy(false);
      }
    },
    [id, reason],
  );

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Guest list refresh</CardTitle>
          <p className="mt-0.5 max-w-2xl text-xs text-muted-foreground">
            The first successful connection loads the full guest list automatically. Refresh it again if the list
            here looks out of date — after PMS maintenance, for example.
          </p>
        </div>
        <span aria-live="polite">
          {active ? (
            <Badge tone="info" dot>Updating automatically</Badge>
          ) : (
            <span className="text-xs text-muted-foreground">Watching for changes</span>
          )}
        </span>
      </CardHeader>

      <CardBody className="space-y-4">
        {/* THE HONEST SENTENCE. Shown only while receiving, because that is the only stage where an operator
            would otherwise expect a total and wonder why there isn't one. */}
        {stage === "RECEIVING" && (
          <Callout tone="info" title="Receiving the guest list">
            The PMS does not say how many records it will send, so there is no percentage to show — any such
            number would be invented. <strong>{(health?.sync_records_received ?? 0).toLocaleString()}</strong>{" "}
            records received so far, waiting for the end-of-list signal.
          </Callout>
        )}
        {(stage === "REQUESTING_FULL_SYNC" || stage === "WAITING_FOR_PMS") && (
          <Callout tone="info" title="Waiting for the PMS to start sending">
            A full refresh has been requested. This also happens automatically the first time the connection
            succeeds — you do not need to do anything.
          </Callout>
        )}
        {stage === "APPLYING" && (
          <Callout tone="info" title="Applying the guest list">
            The list has arrived and is being written in. Room sign-in resumes automatically the moment it
            finishes — usually a few seconds.
          </Callout>
        )}
        {stage === "INTERRUPTED" && (
          <Callout tone="warning" title="The refresh did not finish">
            The previous guest list is still in use — nothing was lost or partly replaced. You can request
            another one.
          </Callout>
        )}
        {stage === "FAILED" && (
          <Callout tone="danger" title="The refresh failed">
            The previous guest list is still in use.
            {health?.sync_failure_code ? ` Reason reported: ${health.sync_failure_code}.` : ""}
          </Callout>
        )}

        <dl className="grid gap-x-8 gap-y-1 text-sm sm:grid-cols-2">
          {/* Connection and sync state are NOT repeated here: the status card above already carries them, and
              two copies of the same fact on one page eventually disagree. This section owns the refresh itself. */}
          <Row label="Stage" value={stage ? STAGE_WORDS[stage] ?? "In progress" : "Nothing running"} />
          <Row label="Last completed refresh" value={when(health?.last_complete_sync_at)} />
          <Row label="Requested" value={when(health?.resync_command_requested_at)} />
          <Row label="PMS started sending" value={when(health?.resync_started_at)} />
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

        <Separator />

        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="secondary"
            disabled={!canRequest || busy}
            onClick={() => { setErr(null); setConfirming(true); }}
          >
            <RefreshCw className={active ? "animate-spin" : undefined} />
            Refresh the guest list now
          </Button>
          {!canRequest && (
            <span className="text-sm text-muted-foreground">
              {health?.transport_status !== "CONNECTED"
                ? "Available once the PMS is connected."
                : "A refresh is already running."}
            </span>
          )}
        </div>

        {note && <Callout tone="success">{note}</Callout>}
        <ErrorBanner err={err} />
      </CardBody>

      <ConfirmDialog
        open={confirming}
        onOpenChange={(v) => { if (!v) setConfirming(false); }}
        title="Reload the full guest list?"
        description="The appliance will ask the PMS to send every in-house guest again, and replace its copy once the list is complete. The current list stays in use until then, so nobody online is interrupted."
        confirmLabel="Request refresh"
        busy={busy}
        error={err}
        requirePassword
        onConfirm={({ password }) => request(password)}
      >
        <Field label="Why are you refreshing?" hint="Recorded with the request.">
          <Select value={reason} onChange={(e) => setReason(e.target.value)}>
            {REASONS.map((r) => (
              <option key={r.value} value={r.value}>{r.label}</option>
            ))}
          </Select>
        </Field>
      </ConfirmDialog>
    </Card>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border py-1.5 last:border-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="text-right font-medium tabular">{value}</dd>
    </div>
  );
}
