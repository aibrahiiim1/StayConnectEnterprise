"use client";

// Protel FIAS sends no record total before the end of a sync, so never show a progress bar, percentage or
// "X of Y": only the real count received so far.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, inBackground, type PmsInterfaceHealth } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Select, Field } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { HelpSection, HelpTip } from "@/components/help";
import { Separator } from "@/components/ui/misc";
import { ConfirmDialog } from "@/components/ui/dialog";
import { RefreshCw } from "lucide-react";

// The server sends the token; the staff wording lives here and an unrecognised token falls back rather than rendering raw.
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

const ACTIVE_STAGES = new Set(["REQUESTING_FULL_SYNC", "WAITING_FOR_PMS", "RECEIVING", "PUBLISHING", "APPLYING"]);

// sync_stage=COMPLETE means published, not usable: the applier writes the Stay tables afterwards and room
// sign-in stays closed until then, so the card derives APPLYING for that gap.
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

  // Poll whenever mounted (slowly when idle, quickly during a sync): the connection coming up and the automatic
  // sync starting happen before any stage is active. One interval whose delay changes, never two effects.
  // onRefreshed is held in a ref because in the dependency array the parent's re-render on every poll would reset the timer before it fires.
  const refreshRef = useRef(onRefreshed);
  refreshRef.current = onRefreshed;
  const delay = active ? 3000 : 10000;
  useEffect(() => {
    const t = setInterval(() => void inBackground(() => refreshRef.current()), delay);
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
          <div className="flex items-center gap-2">
            <CardTitle>Guest list refresh</CardTitle>
            <HelpTip title="Guest list refresh">
              <HelpSection>
                <p>
                  The first successful connection loads the full guest list automatically. Refresh it again if the
                  list here looks out of date — after PMS maintenance, for example.
                </p>
                <p>
                  A refresh asks the PMS to send every in-house guest again. The current list stays in use until the
                  new one is complete, so nobody online is interrupted.
                </p>
              </HelpSection>
            </HelpTip>
          </div>
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
            <RefreshCw className={active ? "animate-spin motion-reduce:animate-none" : undefined} />
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
      <dd className="text-end font-medium tabular">{value}</dd>
    </div>
  );
}
