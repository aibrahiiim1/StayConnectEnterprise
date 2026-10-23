"use client";

// Review decisions require password and reason; they never change a stay or its access directly.
// Automatic expiry applies only to messages received after the setting is enabled.

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";
import { Callout } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { formatDate } from "@/lib/utils";

export const DECISION_WORDS: Record<string, string> = {
  ACKNOWLEDGED_BY_OPERATOR: "Acknowledged",
  LINKED_TO_STAY: "Linked to a stay",
  EXPIRED_UNDECIDED: "Expired",
  DEPARTED_CONFIRMED_BY_ROSTER: "Departure confirmed by the guest list",
  NOT_A_DEPARTURE_ROSTER_SNAPSHOT: "Part of a guest-list refresh",
  ALREADY_CLOSED: "Stay already closed",
  NEEDS_PMS_EVIDENCE: "Waiting for the PMS",
  ACCEPTED_STARTUP_DATA_GAP: "Accepted start-up gap",
};

type Candidate = {
  stay_id: string;
  matched_by: "RESERVATION" | "ROOM";
  room?: string | null;
  status: string;
  arrival?: string | null;
  departure?: string | null;
};

export function ReviewDecisionActions({ eventId, onDecided }: { eventId: string; onDecided: () => void }) {
  const toast = useToast();
  const [mode, setMode] = useState<"" | "ack" | "link">("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [candidates, setCandidates] = useState<Candidate[] | null>(null);
  const [stay, setStay] = useState("");

  useEffect(() => {
    if (mode !== "link") return;
    setCandidates(null);
    setStay("");
    api.get<{ data: Candidate[] }>(`/pms-event-decisions/${eventId}/candidates`)
      .then((r) => setCandidates(r.data ?? []))
      .catch((e) => { setErr(e); setCandidates([]); });
  }, [mode, eventId]);

  const close = () => { setMode(""); setErr(null); };

  async function decide(action: "acknowledge" | "link", reason: string, password: string) {
    setBusy(true);
    setErr(null);
    try {
      await api.post(`/pms-event-decisions/${eventId}/${action}`, {
        reason, password, ...(action === "link" ? { stay_id: stay } : {}),
      });
      toast.success(action === "link" ? "Message linked to the stay" : "Message acknowledged");
      close();
      onDecided();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="secondary" onClick={() => setMode("ack")}>Acknowledge</Button>
        <Button size="sm" variant="secondary" onClick={() => setMode("link")}>Link to a stay</Button>
      </div>
      <ConfirmDialog
        open={mode === "ack"}
        onOpenChange={(v) => { if (!v) close(); }}
        title="Acknowledge this message?"
        description="It stops counting as waiting for a decision. Nothing about any stay or its internet access changes."
        confirmLabel="Acknowledge"
        busy={busy}
        error={err}
        requireReason
        reasonMinLength={3}
        reasonPlaceholder="For example: a test message the PMS vendor sent"
        requirePassword
        onConfirm={({ reason, password }) => decide("acknowledge", reason, password)}
      />
      <ConfirmDialog
        open={mode === "link"}
        onOpenChange={(v) => { if (!v) close(); }}
        title="Link this message to a stay"
        description="Records which stay the message is about. The stay itself does not change: a check-out still only happens when the PMS sends one the appliance can apply."
        confirmLabel="Link"
        busy={busy}
        error={err}
        requireReason
        reasonMinLength={3}
        requirePassword
        onConfirm={({ reason, password }) => (stay ? decide("link", reason, password) : undefined)}
      >
        {candidates === null ? (
          <p className="text-sm text-muted-foreground">Looking for stays this message can be matched to…</p>
        ) : candidates.length === 0 ? (
          <Callout tone="neutral" title="No stay matches this message">
            No stay on this PMS connection carries its reservation, or was in its room when it arrived. Acknowledge
            it instead if it needs no action.
          </Callout>
        ) : (
          <Field label="Stay" required>
            <Select value={stay} onChange={(e) => setStay(e.target.value)}>
              <option value="">Choose a stay…</option>
              {candidates.map((c) => (
                <option key={c.stay_id} value={c.stay_id}>
                  {`Room ${c.room ?? "—"} · ${c.status.replace(/_/g, " ").toLowerCase()} · ${
                    c.arrival ? formatDate(c.arrival) : "?"
                  } – ${c.departure ? formatDate(c.departure) : "?"} · matched by ${
                    c.matched_by === "RESERVATION" ? "reservation" : "room"
                  }`}
                </option>
              ))}
            </Select>
          </Field>
        )}
      </ConfirmDialog>
    </>
  );
}

type ExpirySetting = {
  enabled: boolean;
  expire_after_days: number;
  applies_from?: string | null;
  config_version: number;
  is_default: boolean;
  bounds?: { min_days: number; max_days: number };
};

export function ReviewExpirySettingCard() {
  const toast = useToast();
  const [s, setS] = useState<ExpirySetting | null>(null);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [open, setOpen] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [days, setDays] = useState("30");
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(() => {
    api.get<ExpirySetting>("/pms-event-decisions/settings").then(setS).catch(setLoadErr);
  }, []);
  useEffect(load, [load]);

  const min = s?.bounds?.min_days ?? 1;
  const max = s?.bounds?.max_days ?? 365;
  const n = Number(days);
  const daysOk = Number.isInteger(n) && n >= min && n <= max;

  function edit() {
    if (!s) return;
    setEnabled(s.enabled);
    setDays(String(s.expire_after_days));
    setReason("");
    setPassword("");
    setErr(null);
    setOpen(true);
  }

  async function save() {
    if (!s) return;
    setBusy(true);
    setErr(null);
    try {
      await api.put("/pms-event-decisions/settings", {
        expected_config_version: s.config_version, enabled, expire_after_days: n, reason: reason.trim(), password,
      });
      toast.success("Expiry setting saved");
      setOpen(false);
      load();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
      setPassword("");
    }
  }

  if (loadErr || !s) return null;
  return (
    <Card>
      <CardBody className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="text-sm font-medium">Automatic expiry of waiting messages</div>
          <p className="text-sm text-muted-foreground">
            {s.enabled
              ? `On: a message that arrived after ${s.applies_from ? formatDate(s.applies_from) : "it was switched on"} and has waited more than ${s.expire_after_days} day${s.expire_after_days === 1 ? "" : "s"} is recorded as expired.`
              : "Off. Messages wait until somebody decides them."}{" "}
            Messages that were already waiting when it was switched on are never expired by it.
          </p>
        </div>
        <Button size="sm" variant="secondary" onClick={edit}>Change</Button>
      </CardBody>
      <DialogForm
        open={open}
        onOpenChange={(v) => { if (!busy) setOpen(v); }}
        title="Automatic expiry of waiting messages"
        description="Applies only to messages that arrive after it is switched on. Default: off, 30 days."
        submitLabel="Save"
        busy={busy}
        error={err}
        disabled={!daysOk || reason.trim().length < 3 || password === ""}
        onSubmit={save}
      >
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Expire waiting messages automatically
        </label>
        <Field label="Expire after (days)" hint={`Between ${min} and ${max} days.`} required
          error={daysOk ? undefined : `Enter a whole number of days between ${min} and ${max}.`}>
          <Input type="number" min={min} max={max} value={days} onChange={(e) => setDays(e.target.value)} />
        </Field>
        <Field label="Reason" hint="Recorded in the activity log." required>
          <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
        </Field>
        <Field label="Confirm your password" required>
          <Input type="password" autoComplete="current-password" value={password}
            onChange={(e) => setPassword(e.target.value)} />
        </Field>
      </DialogForm>
    </Card>
  );
}
