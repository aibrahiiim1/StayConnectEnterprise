"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { AlertTriangle, ArrowLeftRight, ArrowRight, History, Radar, RefreshCw } from "lucide-react";
import { api, ApiError, GuestNetwork, ListResp } from "@/lib/api";
import { cn, formatDate } from "@/lib/utils";
import { reasonOk, REASON_HINT, REASON_MAX_CHARS, REASON_MIN_CHARS } from "@/lib/reason";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { usePmsConnections } from "@/lib/pms-scope";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";
import { HelpList, HelpSection } from "@/components/help";
import { DialogForm } from "@/components/ui/dialog";
import { MetricStrip } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { ConsequenceList, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const LINK = "font-medium underline underline-offset-2";
const LIMIT = 200;

type Signal = {
  resolved_at: string;
  outcome_code: string;
  guest_network_id: string;
  occurrences: number;
};

type Preview = {
  from_stay_id: string;
  from_external_reservation_id: string;
  from_room: string;
  from_pms_interface_id: string;
  to_stay_id: string;
  to_external_reservation_id: string;
  to_room: string;
  to_pms_interface_id: string;
  live_devices: number;
  live_sessions: number;
  blocker?: string;
};

type TransferRow = {
  id: string;
  from_external_reservation_id: string;
  from_room: string;
  to_external_reservation_id: string;
  to_room: string;
  created_at: string;
};

type ExecuteResult = {
  transfer_id?: string;
  devices_rebound?: number;
  sessions_rebound?: number;
  window_ends_at?: string;
};

const SIGNAL_NOTICE =
  "Ambiguous sign-ins: a guest's details matched at more than one PMS connection. They are not transfers and are never evidence that a guest moved.";

const OUTCOME_WORDS: Record<string, string> = {
  AMBIGUOUS: "More than one match",
  AMBIGUOUS_ROOM: "Room matched twice",
  AMBIGUOUS_DISCRIMINATOR_REQUIRED: "More than one stay, needs a second detail",
};
const outcomeWords = (c: string) => OUTCOME_WORDS[c] ?? "Not recognised";

const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`;

const NO_PACKAGE = (
  <>
    There is no free internet package the moved access can land on. Add a free package on{" "}
    <Link href="/internet-packages" className={LINK}>Internet packages</Link>, then try again.
  </>
);

const PMS_LINK = <Link href="/pms-interfaces" className={LINK}>PMS connection</Link>;

const BLOCKER_WORDS: [string, React.ReactNode][] = [
  ["room move, not a transfer", "Both stays are on the same PMS connection. That is a room move, not a transfer: the guest keeps their access on their own."],
  ["does not exist yet", "One of the two stays could not be found. Check both references, and that the PMS has sent the new stay."],
  ["already checked out", "The guest has already checked out of the stay they are leaving. Access after check-out cannot be moved."],
  ["not currently in house", "The stay the guest is moving to is not checked in yet. Try again once it is in house."],
  ["already holds live access", "The stay the guest is moving to already has its own internet access. There is nothing to move it into."],
  ["no transferable live access", "The stay the guest is leaving has no internet access to move."],
  ["already been transferred", "This access has already been transferred."],
  ["no eligible no-posting package", NO_PACKAGE],
  ["could not be checked right now", "The transfer could not be checked right now. Try the preview again."],
];

function blockerWords(text: string): React.ReactNode {
  const hit = BLOCKER_WORDS.find(([needle]) => text.includes(needle));
  return hit ? hit[1] : "The appliance refused this transfer. Check both stays, then preview again.";
}

function side(code: string): "leaving" | "moving to" {
  return code.startsWith("from_") ? "leaving" : "moving to";
}

function previewError(e: unknown, multi: boolean): React.ReactNode {
  const code = e instanceof ApiError ? e.code : "";
  if (code.endsWith("_stay_not_found")) {
    return `No stay on this property has the reference entered for the stay the guest is ${side(code)}.`;
  }
  if (code.endsWith("_stay_ambiguous")) {
    return multi
      ? `The reference for the stay the guest is ${side(code)} names more than one stay. Choose its PMS connection, or enter the stay ID.`
      : `The reference for the stay the guest is ${side(code)} names more than one stay. Enter the stay ID instead.`;
  }
  if (code === "invalid") return "Enter both stays, then preview.";
  return "The preview could not be worked out. Check the appliance is reachable, then try again. Nothing has been moved.";
}

const EXECUTE_WORDS: Record<string, React.ReactNode> = {
  reauth_required: "Your password was not accepted.",
  reauth_unavailable: "Your password could not be checked right now. Try again in a moment.",
  reason_required: `Enter a reason of ${REASON_MIN_CHARS} to ${REASON_MAX_CHARS} characters.`,
  invalid: "Enter both stays and a reason, then try again.",
  room_move_not_transfer: BLOCKER_WORDS[0][1],
  destination_missing: BLOCKER_WORDS[1][1],
  source_checked_out: BLOCKER_WORDS[2][1],
  destination_not_in_house: BLOCKER_WORDS[3][1],
  destination_occupied: BLOCKER_WORDS[4][1],
  source_not_transferable: BLOCKER_WORDS[5][1],
  already_transferred: "This access has already been transferred. The list below has been refreshed.",
  no_landing_package: NO_PACKAGE,
};

function executeError(e: unknown): React.ReactNode {
  const code = e instanceof ApiError ? e.code : "";
  const words = EXECUTE_WORDS[code];
  if (words) return <>{words} Nothing was moved.</>;
  if (code.endsWith("_stay_not_found") || code.endsWith("_stay_ambiguous")) {
    return "One of the two stays could no longer be found. Close this, preview again, then confirm. Nothing was moved.";
  }
  return "The transfer could not be completed. Nothing was moved. Check the appliance is reachable, then try again.";
}

function endsAt(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay
    ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

function roomWords(room: string): string {
  return room ? `room ${room}` : "a stay with no room";
}

function StayLabel({ room, reservation, pms, hidden }: { room: string; reservation: string; pms?: string; hidden?: boolean }) {
  return (
    <span className="min-w-0">
      <span className="block font-medium">
        {room ? `Room ${room}` : "No room"}
        {pms ? <span className="font-normal text-muted-foreground"> · {pms}</span> : null}
      </span>
      {reservation ? (
        <span className="block text-caption text-muted-foreground">Reservation {reservation}</span>
      ) : hidden ? (
        <span className="block text-caption text-muted-foreground">Reservation hidden for your role</span>
      ) : null}
    </span>
  );
}

function LoadFailed({ what, onRetry, busy }: { what: string; onRetry: () => void; busy: boolean }) {
  return (
    <div role="alert" className={FADE}>
      <EmptyState
        size="sm"
        icon={<AlertTriangle />}
        title={`${what} could not be loaded`}
        hint="Check the appliance is reachable, then try again. Nothing has been changed."
        action={
          <Button size="sm" variant="secondary" onClick={onRetry} disabled={busy}>
            <RefreshCw className={cn(busy && "animate-spin motion-reduce:animate-none")} /> Try again
          </Button>
        }
      />
    </div>
  );
}

function Capped({ n }: { n: number }) {
  if (n < LIMIT) return null;
  return <p className="border-t border-border px-4 py-2 text-caption text-muted-foreground">Showing the latest {LIMIT.toLocaleString()}.</p>;
}

export function StayTransferView({ canAct, rolesKnown = true }: { canAct: boolean; rolesKnown?: boolean }) {
  const toast = useToast();
  const [signals, setSignals] = useState<Signal[] | null>(null);
  const [signalsFailed, setSignalsFailed] = useState(false);
  const [rows, setRows] = useState<TransferRow[] | null>(null);
  const [rowsFailed, setRowsFailed] = useState(false);
  const [networks, setNetworks] = useState<GuestNetwork[] | null | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [fromStay, setFromStay] = useState("");
  const [toStay, setToStay] = useState("");
  const [fromPms, setFromPms] = useState("");
  const [toPms, setToPms] = useState("");
  const { connections, multi, labelOf } = usePmsConnections();
  const [preview, setPreview] = useState<Preview | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<React.ReactNode>(null);
  const [execError, setExecError] = useState<React.ReactNode>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const [s, t, n] = await Promise.allSettled([
      api.get<{ signals?: Signal[] }>("/stay-transfers/review-signals"),
      api.get<{ transfers?: TransferRow[] }>("/stay-transfers/"),
      api.get<ListResp<GuestNetwork>>("/network/guest-networks"),
    ]);
    if (s.status === "fulfilled") {
      setSignals(s.value.signals ?? []);
      setSignalsFailed(false);
    } else {
      setSignalsFailed(true);
    }
    if (t.status === "fulfilled") {
      setRows(t.value.transfers ?? []);
      setRowsFailed(false);
    } else {
      setRowsFailed(true);
    }
    setNetworks(n.status === "fulfilled" ? n.value.data ?? [] : null);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const live = useMemo(() => (connections ?? []).filter((c) => !c.retired), [connections]);
  const onePms = connections !== null && live.length === 1;
  const reservationsHidden = (rows ?? []).some((r) => !r.from_external_reservation_id && !r.to_external_reservation_id);

  function networkName(id: string): string {
    if (networks === undefined) return "Loading…";
    if (networks === null) return "Not known";
    const n = networks.find((x) => x.id === id);
    if (!n) return "A network that no longer exists";
    return n.name || "Unnamed client network";
  }

  function edit(set: (v: string) => void) {
    return (v: string) => {
      set(v);
      setPreview(null);
      setError(null);
    };
  }

  async function runPreview() {
    setError(null);
    setPreview(null);
    if (!fromStay.trim() || !toStay.trim()) {
      setError("Enter both stays, then preview.");
      return;
    }
    setBusy(true);
    try {
      setPreview(
        await api.post<Preview>("/stay-transfers/preview", {
          from_stay_id: fromStay.trim(),
          to_stay_id: toStay.trim(),
          ...(fromPms ? { from_pms_interface_id: fromPms } : {}),
          ...(toPms ? { to_pms_interface_id: toPms } : {}),
        }),
      );
    } catch (e: unknown) {
      setError(previewError(e, multi));
    } finally {
      setBusy(false);
    }
  }

  function closeConfirm() {
    setConfirming(false);
    setPassword("");
    setReason("");
    setExecError(null);
  }

  async function execute() {
    if (!preview || preview.blocker || !reasonOk(reason) || !password) return;
    setBusy(true);
    setExecError(null);
    try {
      const res = await api.post<ExecuteResult>("/stay-transfers/execute", {
        from_stay_id: preview.from_stay_id,
        to_stay_id: preview.to_stay_id,
        password,
        reason: reason.trim(),
      });
      const until = endsAt(res.window_ends_at);
      toast.success(
        `Access moved to ${roomWords(preview.to_room)}`,
        `${plural(res.devices_rebound ?? preview.live_devices, "device", "devices")} and ${plural(res.sessions_rebound ?? 0, "session", "sessions")} stayed connected through the change.${until ? ` The moved access lasts until ${until}; after that the guest signs in with the new stay.` : ""}`,
      );
      setPreview(null);
      setConfirming(false);
      setReason("");
      setFromStay("");
      setToStay("");
      setFromPms("");
      setToPms("");
      void load();
    } catch (e: unknown) {
      setExecError(executeError(e));
      if (e instanceof ApiError && e.code === "already_transferred") void load();
    } finally {
      setPassword("");
      setBusy(false);
    }
  }

  const spin = cn(loading && "animate-spin motion-reduce:animate-none");
  const ready = preview !== null && !preview.blocker;

  return (
    <PageShell>
      <PageHeader
        title="Cross-PMS transfer"
        description="Move a guest's live access from a stay on one PMS to a stay on another."
        actions={signals !== null || rows !== null ? (
          <Button size="sm" variant="secondary" onClick={() => void load()} disabled={loading}>
            <RefreshCw className={spin} /> Refresh
          </Button>
        ) : undefined}
        help={
          <>
            <HelpSection title="When to use a transfer">
              <p>
                Move a guest&rsquo;s live access from a stay on one PMS to a stay on another — for example, a guest
                moved to the sister property.
              </p>
              <p>
                It is <strong>not</strong> for normal room moves: a guest changing rooms on the same PMS keeps their
                access automatically.
              </p>
            </HelpSection>
            <HelpSection title="How it works">
              <HelpList
                items={[
                  <>Enter the stay reference on the PMS the guest is leaving and the one on the PMS they are moving to, then preview. If a reservation number exists at more than one PMS connection, choose the connection too.</>,
                  <>The preview shows the devices and live sessions that will move. Nothing moves until you confirm with a reason and your password.</>,
                  <>The guest stays connected: no sign-out at the moment of the move. The moved access lasts for a limited time; after that the guest signs in with the new stay.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Review signals">
              <p>
                Ambiguous authentication outcomes from the last 7 days. They are not transfers and are never
                evidence that a guest moved.
              </p>
            </HelpSection>
          </>
        }
      />

      {rolesKnown && !canAct && <ReadOnlyNotice />}

      {(signalsFailed && signals) || (rowsFailed && rows) ? (
        <div className={FADE}>
          <Callout tone="warning">The page could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      ) : null}

      {canAct && onePms && (
        <div className={FADE}>
          <Callout tone="info" title="A transfer needs two PMS connections">
            This property has one PMS connection, so a guest changing rooms keeps their access on their own and there is
            nothing to transfer. Transfers become available once a second PMS is set up on {PMS_LINK}.
          </Callout>
        </div>
      )}

      {canAct && !onePms && (
        <Card>
          <CardHeader>
            <div className="space-y-0.5">
              <CardTitle>Transfer a guest</CardTitle>
              <CardDescription>
                Enter both stays, then preview. Nothing moves until you confirm with a reason and your password.
              </CardDescription>
            </div>
          </CardHeader>
          <CardBody className="space-y-4">
            {error ? (
              <div className={FADE}>
                <Callout tone="danger">{error}</Callout>
              </div>
            ) : null}
            <form
              className="space-y-4"
              onSubmit={(e) => { e.preventDefault(); void runPreview(); }}
            >
              <div className="grid items-start gap-3 sm:grid-cols-[1fr_auto_1fr]">
                <div className="space-y-2">
                  <Field label="From stay" hint="Reservation number on the PMS the guest is leaving, or a stay ID.">
                    <Input
                      value={fromStay}
                      onChange={(e) => edit(setFromStay)(e.target.value)}
                      placeholder="Reservation number"
                      className="font-mono"
                      autoComplete="off"
                    />
                  </Field>
                  {multi && (
                    <Field label="PMS connection of that stay" optional>
                      <Select value={fromPms} onChange={(e) => edit(setFromPms)(e.target.value)}>
                        <option value="">Any PMS connection</option>
                        {live.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
                      </Select>
                    </Field>
                  )}
                </div>
                <ArrowRight className="hidden size-4 text-muted-foreground sm:mt-9 sm:block rtl:rotate-180" aria-hidden />
                <div className="space-y-2">
                  <Field label="To stay" hint="Reservation number on the PMS the guest is moving to, or a stay ID.">
                    <Input
                      value={toStay}
                      onChange={(e) => edit(setToStay)(e.target.value)}
                      placeholder="Reservation number"
                      className="font-mono"
                      autoComplete="off"
                    />
                  </Field>
                  {multi && (
                    <Field label="PMS connection of that stay" optional>
                      <Select value={toPms} onChange={(e) => edit(setToPms)(e.target.value)}>
                        <option value="">Any PMS connection</option>
                        {live.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
                      </Select>
                    </Field>
                  )}
                </div>
              </div>
              <div className="flex justify-end">
                <Button type="submit" variant="secondary" disabled={busy}>
                  {busy && !confirming ? "Previewing…" : "Preview"}
                </Button>
              </div>
            </form>

            {preview && preview.blocker && (
              <div className={FADE}>
                <Callout tone="warning" title="This transfer cannot be performed">
                  {blockerWords(preview.blocker)}
                </Callout>
              </div>
            )}

            {ready && (
              <div className={cn("space-y-3 rounded-lg border border-border bg-surface/50 p-4", FADE)} data-testid="preview-summary">
                <div className="flex flex-wrap items-center gap-3 text-sm">
                  <StayLabel room={preview.from_room} reservation={preview.from_external_reservation_id} pms={multi ? labelOf(preview.from_pms_interface_id) : undefined} />
                  <span role="img" aria-label="moves to" className="inline-flex">
                    <ArrowRight className="size-4 shrink-0 text-muted-foreground rtl:rotate-180" aria-hidden />
                  </span>
                  <StayLabel room={preview.to_room} reservation={preview.to_external_reservation_id} pms={multi ? labelOf(preview.to_pms_interface_id) : undefined} />
                </div>
                <MetricStrip
                  className="sm:grid-cols-2"
                  items={[
                    { label: "Devices that will move", value: preview.live_devices.toLocaleString() },
                    { label: "Live sessions that will move", value: preview.live_sessions.toLocaleString() },
                  ]}
                />
                <p className="text-sm text-muted-foreground">
                  The guest stays connected through the move. The moved access lasts for a limited time; after that the
                  guest signs in with the new stay. Access on the origin stay ends when this completes and is not
                  returned automatically if the guest goes back.
                </p>
              </div>
            )}
          </CardBody>
          {ready && (
            <CardFooter className={cn("justify-end", FADE)}>
              <Button onClick={() => { setExecError(null); setConfirming(true); }}>
                <ArrowLeftRight /> Transfer access…
              </Button>
            </CardFooter>
          )}
        </Card>
      )}

      <Card data-testid="review-signals" className="overflow-hidden">
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle>Review signals</CardTitle>
            <p data-testid="signal-notice" className="text-xs text-muted-foreground">{SIGNAL_NOTICE}</p>
          </div>
        </CardHeader>
        {signalsFailed && !signals ? (
          <LoadFailed what="Review signals" onRetry={() => void load()} busy={loading} />
        ) : signals === null ? (
          <SkeletonRows rows={2} cols={4} />
        ) : signals.length === 0 ? (
          <EmptyState size="sm" icon={<Radar />} title="No ambiguous sign-ins in the last 7 days" />
        ) : (
          <div className={FADE}>
            <Table density="compact" label="Review signals" aria-label="Review signals">
              <THead>
                <TR>
                  <TH>Outcome</TH>
                  <TH>Client network</TH>
                  <TH numeric>Occurrences</TH>
                  <TH>Most recent</TH>
                </TR>
              </THead>
              <TBody>
                {signals.map((s) => (
                  <TR key={`${s.outcome_code}-${s.guest_network_id}`}>
                    <TD nowrap><StatusBadge tone="warn">{outcomeWords(s.outcome_code)}</StatusBadge></TD>
                    <TD>{networkName(s.guest_network_id)}</TD>
                    <TD numeric>{s.occurrences.toLocaleString()}</TD>
                    <TD nowrap muted>{formatDate(s.resolved_at)}</TD>
                  </TR>
                ))}
              </TBody>
            </Table>
            <Capped n={signals.length} />
          </div>
        )}
      </Card>

      <Card className="overflow-hidden">
        <CardHeader>
          <CardTitle>Recorded transfers</CardTitle>
        </CardHeader>
        {rowsFailed && !rows ? (
          <LoadFailed what="Recorded transfers" onRetry={() => void load()} busy={loading} />
        ) : rows === null ? (
          <SkeletonRows rows={3} cols={3} />
        ) : rows.length === 0 ? (
          <EmptyState size="sm" icon={<History />} title="No transfers recorded" hint="Every transfer is kept here with both stays and when it happened." />
        ) : (
          <div className={FADE}>
            <Table density="compact" label="Recorded transfers" aria-label="Recorded transfers">
              <THead>
                <TR>
                  <TH>From</TH>
                  <TH>To</TH>
                  <TH>When</TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((r) => (
                  <TR key={r.id}>
                    <TD><StayLabel room={r.from_room} reservation={r.from_external_reservation_id} hidden={reservationsHidden} /></TD>
                    <TD><StayLabel room={r.to_room} reservation={r.to_external_reservation_id} hidden={reservationsHidden} /></TD>
                    <TD nowrap muted>{formatDate(r.created_at)}</TD>
                  </TR>
                ))}
              </TBody>
            </Table>
            <Capped n={rows.length} />
          </div>
        )}
      </Card>

      <DialogForm
        open={confirming && preview !== null}
        onOpenChange={(v) => { if (!v) closeConfirm(); }}
        title="Transfer this guest's access?"
        description={
          preview
            ? `From ${roomWords(preview.from_room)}${preview.from_external_reservation_id ? ` (reservation ${preview.from_external_reservation_id})` : ""} to ${roomWords(preview.to_room)}${preview.to_external_reservation_id ? ` (reservation ${preview.to_external_reservation_id})` : ""}.`
            : undefined
        }
        size="sm"
        submitLabel="Transfer access"
        busy={busy}
        busyLabel="Transferring…"
        error={execError}
        disabled={!canAct || !reasonOk(reason) || !password}
        disabledReason={!reasonOk(reason) ? "Enter a reason first." : !password ? "Enter your password first." : undefined}
        onSubmit={execute}
      >
        {preview && (
          <ConsequenceList
            tone="warning"
            title="What happens"
            items={[
              `${plural(preview.live_devices, "device", "devices")} and ${plural(preview.live_sessions, "live session", "live sessions")} move to the new stay without disconnecting.`,
              "The moved access lasts for a limited time; after that the guest signs in with the new stay.",
              "Access on the origin stay ends and is not returned automatically.",
            ]}
          />
        )}
        <Field label="Reason" required hint={REASON_HINT}>
          <Input
            value={reason}
            maxLength={REASON_MAX_CHARS}
            onChange={(e) => setReason(e.target.value)}
            placeholder="Guest moved to the sister property"
          />
        </Field>
        <Field label="Confirm your password" required>
          <Input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
      </DialogForm>
    </PageShell>
  );
}
