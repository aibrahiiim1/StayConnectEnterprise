"use client";

// THE POST-STAY IDENTITY SCREEN (Phase 5, DARK).
//
// Two actions, and the screen's main job is to keep them from looking like variants of each other:
//
//   RESET   rotates the credential of an ACTIVE profile. The guest keeps their post-stay access; only the
//           secret changes. This is the answer to "I lost the PIN" — including the case where the guest
//           never received it, because the plaintext exists once and is gone.
//   REVOKE  ENDS post-stay access for that stay episode. It cannot be undone, and the episode gets no
//           replacement profile. The next post-stay identity for that guest exists only after a new stay.
//
// A screen that showed these as two buttons of equal weight would invite an operator to reach for the wrong
// one, so revoke is styled as the destructive action it is, states its consequence in the confirmation, and
// requires the operator to type the word REVOKE alongside their password.
//
// There is no "show PIN" control anywhere here, and its absence is deliberate: the appliance does not store
// the plaintext and cannot produce it. The only thing that ever returns one is a reset, once, in that
// response — which is why the reveal is the kit's OneTimeReveal and cannot be reopened.
//
// THE ROLE DECIDES WHETHER THE BUTTONS EXIST, not whether they are greyed out. A read-only operator sees the
// list and a one-line notice; edged refuses the action anyway, so a disabled button was only ever a promise
// the screen could not keep.

import { useCallback, useEffect, useMemo, useState } from "react";
import { CalendarClock, KeyRound, ShieldOff } from "lucide-react";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DetailDialog, DialogForm } from "@/components/ui/dialog";
import { KeyValueGrid } from "@/components/ui/data";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { ConsequenceList, OneTimeReveal, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";

type Profile = {
  id: string;
  stay_id: string;
  origin_lifecycle_version: number;
  external_reservation_id: string;
  normalized_room_number: string | null;
  stay_status: string;
  status: "ACTIVE" | "REVOKED";
  pin_generation: number;
  issued_via: string;
  issued_at: string;
  valid_until: string;
  revoked_at: string | null;
  revoke_reason: string | null;
  authenticable: boolean;
};

type Revealed = { profileId: string; pin: string; validUntil: string };

// The stay's lifecycle, in the words Stays uses. The wire value is kept for anything this table was not taught.
const STAY_WORDS: Record<string, string> = {
  IN_HOUSE: "In house",
  RESERVED: "Arriving",
  CHECKED_OUT: "Checked out",
  POST_STAY_ACTIVE: "Post-stay access",
  CANCELLED: "Cancelled",
  NO_SHOW: "No show",
};
const stayWords = (s: string) => STAY_WORDS[s] ?? s.replace(/_/g, " ").toLowerCase();

type StateWords = { label: string; tone: "ok" | "warn" | "err" | "default"; hint?: string };
function stateOf(row: Profile): StateWords {
  if (row.status === "REVOKED") return { label: "Ended", tone: "default", hint: "Access ended for this stay" };
  if (row.authenticable) return { label: "Active", tone: "ok" };
  // ACTIVE but not authenticable: expired, or the stay moved to a new episode. Saying which matters here —
  // this is the operator's screen, and the guest sees nothing either way.
  return { label: "Active, not usable", tone: "warn", hint: "Expired, or the stay moved on" };
}

/** `rolesKnown` is false while the route shell is still reading the operator's roles, so the read-only notice
 *  does not flash at an operator who can act. */
export function PostStayView({ canAct, rolesKnown = true }: { canAct: boolean; rolesKnown?: boolean }) {
  const toast = useToast();
  const [rows, setRows] = useState<Profile[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<{ kind: "reset" | "revoke"; row: Profile } | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("");
  const [confirmWord, setConfirmWord] = useState("");
  const [revealed, setRevealed] = useState<Revealed | null>(null);
  const [detail, setDetail] = useState<Profile | null>(null);

  const load = useCallback(() => {
    api
      .get<{ profiles?: Profile[] }>("/post-stay-profiles/")
      .then((m) => {
        setRows(m.profiles ?? []);
        setError(null);
      })
      .catch((e) => {
        setError(String(e?.message ?? e));
        // Keep the last good list: an error banner above real rows is honest, an empty table is not.
        setRows((prev) => prev ?? []);
      });
  }, []);

  useEffect(load, [load]);

  const closeDialog = () => {
    setDialog(null);
    setDialogError(null);
    setPassword("");
    setReason("");
    setConfirmWord("");
  };

  // Revoke additionally requires the word, because a password prompt alone is muscle memory by the third
  // time an operator sees it and this action has no undo.
  const canSubmit = useMemo(() => {
    if (!dialog) return false;
    if (password.length === 0 || reason.trim().length < 4) return false;
    if (dialog.kind === "revoke" && confirmWord.trim().toUpperCase() !== "REVOKE") return false;
    return true;
  }, [dialog, password, reason, confirmWord]);

  async function submit() {
    if (!dialog || !canSubmit) return;
    setBusy(true);
    setDialogError(null);
    try {
      const path = `/post-stay-profiles/${dialog.row.id}/${dialog.kind}`;
      const res = await api.post<{ pin?: string; valid_until?: string }>(path, {
        password,
        reason: reason.trim(),
      });
      if (dialog.kind === "reset" && res.pin) {
        setRevealed({ profileId: dialog.row.id, pin: res.pin, validUntil: res.valid_until ?? "" });
      } else if (dialog.kind === "revoke") {
        toast.success("Post-stay access ended", `Room ${dialog.row.normalized_room_number ?? "—"} can no longer reconnect with a PIN.`);
      }
      closeDialog();
      load();
    } catch (e: unknown) {
      setDialogError(String((e as { message?: string })?.message ?? e));
    } finally {
      setBusy(false);
    }
  }

  const counts = useMemo(() => {
    const list = rows ?? [];
    return {
      usable: list.filter((r) => r.status === "ACTIVE" && r.authenticable).length,
      notUsable: list.filter((r) => r.status === "ACTIVE" && !r.authenticable).length,
      ended: list.filter((r) => r.status === "REVOKED").length,
    };
  }, [rows]);

  const actionable = canAct && (rows ?? []).some((r) => r.status === "ACTIVE");

  return (
    <PageShell>
      <PageHeader
        eyebrow="Guests"
        title="Post-stay access"
        icon={<CalendarClock />}
        description="After checkout a guest can reconnect with a PIN for a limited time. Reset a lost PIN or end access here. A PIN belongs to one stay, never to a room: when the room is re-let, the previous PIN stops working on its own."
      />

      {rolesKnown && !canAct && <ReadOnlyNotice />}
      <ErrorBanner err={error} />

      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard label="Can reconnect now" value={rows ? counts.usable.toLocaleString() : "—"} tone="ok" icon={<KeyRound />} />
        <StatCard
          label="Active, not usable"
          value={rows ? counts.notUsable.toLocaleString() : "—"}
          hint="Expired, or the stay moved on"
        />
        <StatCard label="Ended by staff" value={rows ? counts.ended.toLocaleString() : "—"} icon={<ShieldOff />} />
      </div>

      <Card className="overflow-hidden">
        {rows === null ? (
          <SkeletonRows rows={4} cols={6} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<CalendarClock />}
            title="No post-stay access yet"
            hint="A departing guest who is given post-stay access appears here with their room and how long the PIN is valid."
          />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Room</TH>
                <TH className="hidden md:table-cell">Reservation</TH>
                <TH className="hidden lg:table-cell">Stay</TH>
                <TH>State</TH>
                <TH className="hidden sm:table-cell">PIN</TH>
                <TH>Valid until</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((row) => {
                const st = stateOf(row);
                return (
                  <TR key={row.id}>
                    <TD>
                      <button
                        type="button"
                        onClick={() => setDetail(row)}
                        className="text-start font-medium hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                      >
                        {row.normalized_room_number ? `Room ${row.normalized_room_number}` : "No room"}
                      </button>
                    </TD>
                    <TD className="hidden text-sm md:table-cell">{row.external_reservation_id || "—"}</TD>
                    <TD className="hidden text-sm text-muted-foreground lg:table-cell">
                      {stayWords(row.stay_status)}
                      <div className="text-caption">Stay episode {row.origin_lifecycle_version}</div>
                    </TD>
                    <TD>
                      <Badge tone={st.tone} dot>{st.label}</Badge>
                      {st.hint && <div className="mt-0.5 text-caption text-muted-foreground">{st.hint}</div>}
                    </TD>
                    <TD className="hidden text-sm tabular text-muted-foreground sm:table-cell">
                      {row.pin_generation === 1 ? "First PIN" : `Reset ${row.pin_generation - 1}×`}
                    </TD>
                    <TD className="whitespace-nowrap text-sm text-muted-foreground">{formatDate(row.valid_until)}</TD>
                    <TD className="whitespace-nowrap text-end">
                      {canAct && row.status === "ACTIVE" ? (
                        <div className="flex justify-end gap-1.5">
                          <Button size="sm" variant="secondary" onClick={() => setDialog({ kind: "reset", row })}>
                            Reset PIN
                          </Button>
                          <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setDialog({ kind: "revoke", row })}>
                            End access
                          </Button>
                        </div>
                      ) : (
                        <Button size="sm" variant="ghost" onClick={() => setDetail(row)}>Details</Button>
                      )}
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>

      {actionable && (
        <p className="text-caption text-muted-foreground">
          The PIN itself is never stored, so it cannot be shown again. A guest who lost it gets a new one with Reset PIN
          and keeps their access.
        </p>
      )}

      {/* ------------------------------------------------------------------ details */}
      <DetailDialog
        open={detail !== null}
        onOpenChange={(v) => !v && setDetail(null)}
        title={detail?.normalized_room_number ? `Room ${detail.normalized_room_number}` : "Post-stay access"}
        description={detail ? `Reservation ${detail.external_reservation_id || "—"}` : undefined}
        size="md"
      >
        {detail && (
          <>
            <div className="flex flex-wrap gap-2">
              <Badge tone={stateOf(detail).tone} dot>{stateOf(detail).label}</Badge>
              <Badge tone="neutral">{stayWords(detail.stay_status)}</Badge>
            </div>
            {detail.status === "REVOKED" && (
              <Callout tone="neutral" title="Access was ended for this stay">
                {detail.revoke_reason ? <>Reason recorded: {detail.revoke_reason}</> : "No reason was recorded."}
              </Callout>
            )}
            <KeyValueGrid
              items={[
                { label: "Stay episode", value: String(detail.origin_lifecycle_version) },
                { label: "PIN generation", value: String(detail.pin_generation) },
                { label: "Issued", value: formatDate(detail.issued_at) },
                { label: "Issued via", value: detail.issued_via ? detail.issued_via.replace(/_/g, " ").toLowerCase() : "—" },
                { label: "Valid until", value: formatDate(detail.valid_until) },
                { label: "Ended", value: detail.revoked_at ? formatDate(detail.revoked_at) : "—" },
                { label: "Stay reference", value: <MonoId value={detail.stay_id} title="Stay" /> },
                { label: "Access reference", value: <MonoId value={detail.id} title="Post-stay access" /> },
              ]}
            />
          </>
        )}
      </DetailDialog>

      {/* ------------------------------------------------------------------ reset / end access */}
      <DialogForm
        open={dialog !== null}
        onOpenChange={(v) => { if (!v) closeDialog(); }}
        title={dialog?.kind === "revoke" ? "End post-stay access" : "Reset the PIN"}
        description={
          dialog?.kind === "revoke"
            ? `Room ${dialog.row.normalized_room_number ?? "—"} · reservation ${dialog.row.external_reservation_id}`
            : "A new PIN replaces the old one immediately. The guest keeps their post-stay access; only the secret changes. The new PIN is shown once, on this screen."
        }
        size="sm"
        submitLabel={dialog?.kind === "revoke" ? "End access permanently" : "Reset PIN"}
        submitVariant={dialog?.kind === "revoke" ? "danger" : "primary"}
        busy={busy}
        busyLabel={dialog?.kind === "revoke" ? "Ending…" : "Resetting…"}
        error={dialogError}
        disabled={!canSubmit}
        onSubmit={submit}
      >
        {dialog?.kind === "revoke" && (
          <ConsequenceList
            items={[
              `Post-stay access ends for this stay (episode ${dialog.row.origin_lifecycle_version}).`,
              "This stay gets no replacement PIN.",
              "If the guest only lost their PIN, reset it instead.",
            ]}
          />
        )}
        <Field label="Reason" required hint="Recorded in the activity log. At least 4 characters.">
          <Input
            value={reason}
            maxLength={500}
            onChange={(e) => setReason(e.target.value)}
            placeholder={dialog?.kind === "reset" ? "Guest lost the printout" : "Guest asked us to end it"}
          />
        </Field>
        {dialog?.kind === "revoke" && (
          <Field label={<>Type <span className="font-mono font-bold">REVOKE</span> to confirm</>} required>
            <Input
              value={confirmWord}
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              onChange={(e) => setConfirmWord(e.target.value)}
            />
          </Field>
        )}
        <Field label="Confirm your password" required>
          <Input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
      </DialogForm>

      {/* ------------------------------------------------------------------ the one-time PIN */}
      <OneTimeReveal
        open={revealed !== null}
        title="New PIN — shown once"
        description="Give this to the guest now. It is not stored and cannot be shown again."
        value={revealed?.pin ?? ""}
        valueLabel="Post-stay PIN"
        acknowledgeLabel="I have given it to the guest"
        onAcknowledge={() => setRevealed(null)}
      >
        {revealed?.validUntil && (
          <p className="text-sm text-muted-foreground">Valid until {formatDate(revealed.validUntil)}.</p>
        )}
        <p className="text-caption text-muted-foreground">
          If it is lost before it reaches the guest, reset again — the guest keeps their access either way.
        </p>
      </OneTimeReveal>
    </PageShell>
  );
}
