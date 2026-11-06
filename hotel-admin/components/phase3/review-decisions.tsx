"use client";

import { useCallback, useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { OneGateLoader } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { Checkbox, Field, Input, InputGroup, Select } from "@/components/ui/input";
import { StatusBadge } from "@/components/ui/badge";
import { Callout } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { cn, formatDate } from "@/lib/utils";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

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

const STAY_STATUS_WORDS: Record<string, string> = {
  IN_HOUSE: "In house",
  RESERVED: "Arriving",
  CHECKED_OUT: "Checked out",
  POST_STAY_ACTIVE: "After check-out access",
  CANCELLED: "Cancelled",
  NO_SHOW: "No show",
};

export const stayStatusWords = (s: string) => STAY_STATUS_WORDS[s] ?? "Status not recognised";

type Candidate = {
  stay_id: string;
  matched_by: "RESERVATION" | "ROOM";
  room?: string | null;
  status: string;
  arrival?: string | null;
  departure?: string | null;
};

const GONE = new Set(["already_decided", "not_waiting", "not_found"]);

function decisionError(e: unknown): string {
  if (!(e instanceof ApiError)) return "The decision could not be recorded. Nothing has been changed. Try again.";
  switch (e.code) {
    case "already_decided":
      return "Somebody else has already decided this message.";
    case "not_waiting":
      return "This message is no longer waiting for a decision.";
    case "not_found":
      return "This message could not be found any more.";
    case "not_matchable":
      return "That stay cannot be linked to this message. Choose another stay, or acknowledge the message instead.";
    case "stay_required":
      return "Choose the stay this message is about.";
    case "reason_required":
      return "Enter a reason of 3 to 500 characters.";
    case "reauth_required":
    case "unauthorized":
      return "Your password was not accepted. Enter the password you sign in with.";
    default:
      return "The decision could not be recorded. Nothing has been changed. Try again.";
  }
}

function stayChoice(c: Candidate) {
  const from = c.arrival ? formatDate(c.arrival) : "arrival not given";
  const to = c.departure ? formatDate(c.departure) : "departure not given";
  return `Room ${c.room ?? "not given"} · ${stayStatusWords(c.status)} · ${from} to ${to} · matched by ${
    c.matched_by === "RESERVATION" ? "reservation" : "room"
  }`;
}

export function ReviewDecisionActions({ eventId, onDecided }: { eventId: string; onDecided: () => void }) {
  const toast = useToast();
  const [mode, setMode] = useState<"" | "ack" | "link">("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [candidates, setCandidates] = useState<Candidate[] | null>(null);
  const [candidatesFailed, setCandidatesFailed] = useState(false);
  const [stay, setStay] = useState("");

  const loadCandidates = useCallback(() => {
    setCandidates(null);
    setCandidatesFailed(false);
    setStay("");
    api.get<{ data: Candidate[] }>(`/pms-event-decisions/${eventId}/candidates`)
      .then((r) => setCandidates(r.data ?? []))
      .catch(() => setCandidatesFailed(true));
  }, [eventId]);

  useEffect(() => {
    if (mode === "link") loadCandidates();
  }, [mode, loadCandidates]);

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
      if (e instanceof ApiError && GONE.has(e.code)) {
        toast.error(decisionError(e), "The list has been refreshed. Nothing was changed by you.");
        close();
        onDecided();
        return;
      }
      setErr(decisionError(e));
    } finally {
      setBusy(false);
    }
  }

  function confirmLink(reason: string, password: string) {
    if (candidatesFailed || candidates === null) {
      setErr("The stays this message can be linked to are not loaded yet.");
      return;
    }
    if (candidates.length === 0) {
      setErr("There is no stay to link this message to. Acknowledge it instead if it needs no action.");
      return;
    }
    if (!stay) {
      setErr("Choose the stay this message is about.");
      return;
    }
    return decide("link", reason, password);
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
        requirePassword strictPassword
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
        requirePassword strictPassword
        onConfirm={({ reason, password }) => confirmLink(reason, password)}
      >
        {candidatesFailed ? (
          <Callout tone="danger" title="The matching stays could not be loaded" className={FADE}>
            Nothing has been changed.{" "}
            <Button variant="link" size="xs" className="h-auto p-0 font-medium" onClick={loadCandidates}>
              <RefreshCw className="size-3.5" aria-hidden /> Try again
            </Button>
          </Callout>
        ) : candidates === null ? (
          <div role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
            <OneGateLoader className="size-4" label="Looking for stays this message can be linked to" />
            Looking for stays this message can be linked to
          </div>
        ) : candidates.length === 0 ? (
          <Callout tone="neutral" title="No stay matches this message" className={FADE}>
            No stay on this PMS connection carries its reservation, or was in its room when it arrived. Acknowledge
            it instead if it needs no action.
          </Callout>
        ) : (
          <div className={FADE}>
            <Field label="Stay" required>
              <Select value={stay} onChange={(e) => { setStay(e.target.value); setErr(null); }}>
                <option value="">Choose a stay…</option>
                {candidates.map((c) => (
                  <option key={c.stay_id} value={c.stay_id}>{stayChoice(c)}</option>
                ))}
              </Select>
            </Field>
          </div>
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

function expiryError(e: unknown, min: number, max: number): string {
  if (!(e instanceof ApiError)) return "The setting could not be saved. Nothing has been changed. Try again.";
  switch (e.code) {
    case "version_conflict":
      return "Somebody changed this setting after you opened it. The current setting has been loaded; check it and save again.";
    case "out_of_bounds":
      return `Enter a whole number of days between ${min} and ${max}.`;
    case "reason_required":
      return "Enter a reason of 3 to 500 characters.";
    case "reauth_required":
    case "unauthorized":
      return "Your password was not accepted. Enter the password you sign in with.";
    case "settings_rejected":
      return "The appliance did not accept this setting. Nothing has been changed.";
    default:
      return "The setting could not be saved. Nothing has been changed. Try again.";
  }
}

export function ReviewExpirySettingCard() {
  const toast = useToast();
  const [s, setS] = useState<ExpirySetting | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [days, setDays] = useState("30");
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    return api.get<ExpirySetting>("/pms-event-decisions/settings")
      .then((v) => { setS(v); setLoadFailed(false); })
      .catch(() => setLoadFailed(true))
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => { void load(); }, [load]);

  const min = s?.bounds?.min_days ?? 1;
  const max = s?.bounds?.max_days ?? 365;
  const n = Number(days);
  const daysOk = Number.isInteger(n) && n >= min && n <= max;
  const reasonOk = reason.trim().length >= 3;
  const blocked = !daysOk
    ? `Enter a whole number of days between ${min} and ${max}.`
    : !reasonOk
      ? "Enter a reason of at least 3 characters."
      : password === ""
        ? "Enter your password to save."
        : undefined;

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
      void load();
    } catch (e) {
      setErr(expiryError(e, min, max));
      if (e instanceof ApiError && e.code === "version_conflict") void load();
    } finally {
      setBusy(false);
      setPassword("");
    }
  }

  if (!s && loadFailed) {
    return (
      <Card className={FADE}>
        <CardBody className="flex flex-wrap items-center justify-between gap-3 py-3">
          <div className="min-w-0 space-y-0.5" role="alert">
            <span className="text-emphasis">Automatic expiry of waiting messages</span>
            <p className="text-xs text-muted-foreground">This setting could not be read. Nothing has been changed.</p>
          </div>
          <Button size="sm" variant="secondary" disabled={loading} onClick={() => void load()}>
            <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
          </Button>
        </CardBody>
      </Card>
    );
  }
  if (!s) return null;
  return (
    <Card className={FADE}>
      <CardBody className="flex flex-wrap items-center justify-between gap-3 py-3">
        <div className="min-w-0 space-y-0.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-emphasis">Automatic expiry of waiting messages</span>
            <StatusBadge tone={s.enabled ? "ok" : "default"}>{s.enabled ? "On" : "Off"}</StatusBadge>
          </div>
          <p className="text-xs text-muted-foreground">
            {s.enabled
              ? `A message that arrived after ${s.applies_from ? formatDate(s.applies_from) : "it was switched on"} and has waited more than ${s.expire_after_days} day${s.expire_after_days === 1 ? "" : "s"} is recorded as expired.`
              : "Messages wait until somebody decides them."}{" "}
            Messages already waiting when it was switched on are never expired by it.
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
        disabled={blocked !== undefined}
        disabledReason={blocked}
        onSubmit={save}
      >
        <Checkbox label="Expire waiting messages automatically" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        <Field label="Expire after (days)" hint={`Between ${min} and ${max} days.`} required
          error={daysOk ? undefined : `Enter a whole number of days between ${min} and ${max}.`}>
          <InputGroup suffix="days" className="w-48">
            <Input type="number" inputMode="numeric" min={min} max={max} value={days} onChange={(e) => setDays(e.target.value)} />
          </InputGroup>
        </Field>
        <Field label="Reason" hint="At least 3 characters. Recorded in the activity log." required>
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
