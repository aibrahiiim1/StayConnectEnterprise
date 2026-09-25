"use client";

// THE CROSS-PMS TRANSFER SCREEN (Phase 5, DARK).
//
// A transfer ENDS a guest's access on one property and re-establishes it on another. The screen is built so
// that the operator sees what that means before they can do it:
//
//   * the review signals are shown and labelled as signals. They are ambiguous authentication outcomes, not
//     transfers, and the panel says so in the words the API returns. There is deliberately no "transfer this
//     one" button anywhere near them — an operator who could act directly from a signal would be acting on an
//     inference nobody made.
//   * the operator must PREVIEW before the confirm control appears at all. The preview names both rooms and
//     counts the devices and sessions about to move, so the confirmation is a decision rather than a sentence.
//   * a blocker is shown as plain language before the operator types anything. "This is a room move, not a
//     transfer" costs one screen instead of a completed dialog and a rejected submission.
//
// Raw identifiers (network ids, stay ids) are never the main content: they sit in copyable chips for the one
// moment someone needs to quote them, and dates are in the operator's locale.

import { useCallback, useEffect, useState } from "react";
import { ArrowLeftRight, ArrowRight, History, Radar } from "lucide-react";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm } from "@/components/ui/dialog";
import { MetricStrip } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { ConsequenceList, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";

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

// The ambiguous outcomes, in the words Guest sign-in checks uses. An unknown code is de-underscored rather
// than hidden: a signal this table was not taught must still be countable.
const OUTCOME_WORDS: Record<string, string> = {
  AMBIGUOUS: "More than one match",
  AMBIGUOUS_ROOM: "Room matched twice",
  AMBIGUOUS_DISCRIMINATOR_REQUIRED: "More than one stay — needs a second detail",
};
const outcomeWords = (c: string) => OUTCOME_WORDS[c] ?? c.replace(/_/g, " ").toLowerCase();

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

function StayLabel({ room, reservation }: { room: string; reservation: string }) {
  return (
    <span className="min-w-0">
      <span className="block font-medium">{room ? `Room ${room}` : "No room"}</span>
      <span className="block text-caption text-muted-foreground">Reservation {reservation || "—"}</span>
    </span>
  );
}

/** `rolesKnown` is false while the route shell is still reading the operator's roles, so the read-only notice
 *  does not flash at an operator who can act. */
export function StayTransferView({ canAct, rolesKnown = true }: { canAct: boolean; rolesKnown?: boolean }) {
  const toast = useToast();
  const [signals, setSignals] = useState<Signal[] | null>(null);
  const [signalNotice, setSignalNotice] = useState("");
  const [rows, setRows] = useState<TransferRow[] | null>(null);
  const [fromStay, setFromStay] = useState("");
  const [toStay, setToStay] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [execError, setExecError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api
      .get<{ signals?: Signal[]; notice?: string }>("/stay-transfers/review-signals")
      .then((m) => {
        setSignals(m.signals ?? []);
        setSignalNotice(m.notice ?? "");
      })
      .catch(() => setSignals([]));
    api
      .get<{ transfers?: TransferRow[] }>("/stay-transfers/")
      .then((m) => setRows(m.transfers ?? []))
      .catch(() => setRows([]));
  }, []);

  useEffect(load, [load]);

  async function runPreview() {
    setError(null);
    setPreview(null);
    if (!fromStay.trim() || !toStay.trim()) {
      setError("Both stays are required.");
      return;
    }
    setBusy(true);
    try {
      setPreview(
        await api.post<Preview>("/stay-transfers/preview", {
          from_stay_id: fromStay.trim(),
          to_stay_id: toStay.trim(),
        }),
      );
    } catch (e: unknown) {
      setError(String((e as { message?: string })?.message ?? e));
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
    if (!preview || preview.blocker || reason.trim().length < 4 || !password) return;
    setBusy(true);
    setExecError(null);
    try {
      const res = await api.post<{ transfer_id?: string; sessions_rebound?: number }>(
        "/stay-transfers/execute",
        {
          from_stay_id: preview.from_stay_id,
          to_stay_id: preview.to_stay_id,
          password,
          reason: reason.trim(),
        },
      );
      toast.success(
        "Access moved",
        `${plural(res.sessions_rebound ?? 0, "session", "sessions")} stayed connected through the change.`,
      );
      setPreview(null);
      setConfirming(false);
      setPassword("");
      setReason("");
      setFromStay("");
      setToStay("");
      load();
    } catch (e: unknown) {
      setExecError(String((e as { message?: string })?.message ?? e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <PageShell>
      <PageHeader
        eyebrow="Property management system"
        title="Cross-PMS transfer"
        icon={<ArrowLeftRight />}
        description="Move a guest's live access from a stay on one PMS to a stay on another — for example, a guest moved to the sister property. Not for normal room moves: a guest changing rooms on the same PMS keeps their access automatically."
      />

      {rolesKnown && !canAct && <ReadOnlyNotice />}

      {/* ------------------------------------------------------------------ the transfer form */}
      {canAct && (
        <Card>
          <CardHeader>
            <div className="space-y-1">
              <CardTitle>Transfer a guest</CardTitle>
              <CardDescription>
                Enter both stays, then preview. Nothing moves until you confirm with a reason and your password.
              </CardDescription>
            </div>
          </CardHeader>
          <CardBody className="space-y-4">
            <ErrorBanner err={error} className="mb-0" />
            <form
              className="grid items-end gap-3 sm:grid-cols-[1fr_auto_1fr_auto]"
              onSubmit={(e) => { e.preventDefault(); void runPreview(); }}
            >
              <Field label="From stay" hint="The stay reference on the PMS the guest is leaving.">
                <Input
                  value={fromStay}
                  onChange={(e) => { setFromStay(e.target.value); setPreview(null); }}
                  placeholder="Stay on the origin PMS"
                  className="font-mono"
                  autoComplete="off"
                />
              </Field>
              <ArrowRight className="mb-9 hidden size-4 text-muted-foreground sm:block rtl:rotate-180" aria-hidden />
              <Field label="To stay" hint="The stay reference on the PMS the guest is moving to.">
                <Input
                  value={toStay}
                  onChange={(e) => { setToStay(e.target.value); setPreview(null); }}
                  placeholder="Stay on the destination PMS"
                  className="font-mono"
                  autoComplete="off"
                />
              </Field>
              <Button type="submit" variant="secondary" className="sm:mb-6" disabled={busy}>
                {busy && !confirming ? "Previewing…" : "Preview"}
              </Button>
            </form>

            {preview && preview.blocker && (
              <Callout tone="warning" title="This transfer cannot be performed">
                {preview.blocker}
              </Callout>
            )}

            {preview && !preview.blocker && (
              <div className="space-y-3 rounded-lg border border-border bg-surface/50 p-4" data-testid="preview-summary">
                <div className="flex flex-wrap items-center gap-3 text-sm">
                  <StayLabel room={preview.from_room} reservation={preview.from_external_reservation_id} />
                  <ArrowRight className="size-4 shrink-0 text-muted-foreground rtl:rotate-180" aria-label="moves to" />
                  <StayLabel room={preview.to_room} reservation={preview.to_external_reservation_id} />
                </div>
                <MetricStrip
                  className="sm:grid-cols-2"
                  items={[
                    { label: "Devices that will move", value: preview.live_devices.toLocaleString() },
                    { label: "Live sessions that will move", value: preview.live_sessions.toLocaleString() },
                  ]}
                />
                <p className="text-sm text-muted-foreground">
                  The guest stays connected: no sign-out, no signing in again. Access on the origin stay ends when this
                  completes and is not returned automatically if the guest goes back.
                </p>
              </div>
            )}
          </CardBody>
          {preview && !preview.blocker && (
            <CardFooter className="justify-end">
              <Button onClick={() => { setExecError(null); setConfirming(true); }}>
                <ArrowLeftRight /> Transfer access…
              </Button>
            </CardFooter>
          )}
        </Card>
      )}

      {/* ------------------------------------------------------------------ review signals */}
      <Card data-testid="review-signals">
        <CardHeader>
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2"><Radar className="size-4 text-muted-foreground" aria-hidden /> Review signals</CardTitle>
            {/* The API's own words, rendered rather than paraphrased: a screen that softened them would be the
                place the "ambiguity means the guest moved" habit starts. */}
            <CardDescription data-testid="signal-notice">
              {signalNotice ||
                "Ambiguous authentication outcomes. They are not transfers and are never evidence that a guest moved."}
            </CardDescription>
          </div>
        </CardHeader>
        {signals === null ? (
          <SkeletonRows rows={2} cols={4} />
        ) : signals.length === 0 ? (
          <EmptyState icon={<Radar />} title="No ambiguous sign-ins in the last 7 days" />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Outcome</TH>
                <TH>Guest network</TH>
                <TH className="text-end">Occurrences</TH>
                <TH>Most recent</TH>
              </TR>
            </THead>
            <TBody>
              {signals.map((s) => (
                <TR key={`${s.outcome_code}-${s.guest_network_id}`}>
                  <TD><Badge tone="warn">{outcomeWords(s.outcome_code)}</Badge></TD>
                  {/* Plain text, not the copyable chip: this section is evidence with nothing to act on, and even a
                      copy button would be a control inside it. The full id is in the tooltip. */}
                  <TD>
                    <span className="font-mono text-caption text-muted-foreground" title={`Guest network ${s.guest_network_id}`}>
                      {s.guest_network_id.length > 12 ? `${s.guest_network_id.slice(0, 8)}…` : s.guest_network_id}
                    </span>
                  </TD>
                  <TD className="text-end tabular">{s.occurrences.toLocaleString()}</TD>
                  <TD className="whitespace-nowrap text-sm text-muted-foreground">{formatDate(s.resolved_at)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      {/* ------------------------------------------------------------------ recorded transfers */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2"><History className="size-4 text-muted-foreground" aria-hidden /> Recorded transfers</CardTitle>
        </CardHeader>
        {rows === null ? (
          <SkeletonRows rows={3} cols={3} />
        ) : rows.length === 0 ? (
          <EmptyState icon={<History />} title="No transfers recorded" hint="Every transfer is kept here with both stays and when it happened." />
        ) : (
          <Table>
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
                  <TD><StayLabel room={r.from_room} reservation={r.from_external_reservation_id} /></TD>
                  <TD><StayLabel room={r.to_room} reservation={r.to_external_reservation_id} /></TD>
                  <TD className="whitespace-nowrap text-sm text-muted-foreground">{formatDate(r.created_at)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      {/* ------------------------------------------------------------------ confirm */}
      <DialogForm
        open={confirming && preview !== null}
        onOpenChange={(v) => { if (!v) closeConfirm(); }}
        title="Transfer this guest's access?"
        description={
          preview
            ? `Room ${preview.from_room || "—"} (reservation ${preview.from_external_reservation_id}) to room ${preview.to_room || "—"} (reservation ${preview.to_external_reservation_id}).`
            : undefined
        }
        size="sm"
        submitLabel="Transfer access"
        busy={busy}
        busyLabel="Transferring…"
        error={execError}
        disabled={!canAct || reason.trim().length < 4 || !password}
        onSubmit={execute}
      >
        {preview && (
          <ConsequenceList
            tone="warning"
            title="What happens"
            items={[
              `${plural(preview.live_devices, "device", "devices")} and ${plural(preview.live_sessions, "live session", "live sessions")} move to the new stay without disconnecting.`,
              "Access on the origin stay ends and is not returned automatically.",
            ]}
          />
        )}
        <Field label="Reason" required hint="Recorded in the activity log. At least 4 characters.">
          <Input
            value={reason}
            maxLength={500}
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
