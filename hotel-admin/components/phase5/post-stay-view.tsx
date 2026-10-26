"use client";

// Reset rotates the PIN without ending access; revoke ends access for this stay episode irreversibly.
// Plaintext PINs are never stored, so a reset response can reveal its new PIN only once.

import { usePmsConnections } from "@/lib/pms-scope";
import { useCallback, useEffect, useMemo, useState } from "react";
import { CalendarClock } from "lucide-react";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Checkbox, Field, Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm } from "@/components/ui/dialog";
import { DetailsDrawer } from "@/components/ui/details-drawer";
import { RowActions } from "@/components/ui/row-actions";
import { DescriptionList } from "@/components/ui/data";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { ConsequenceList, OneTimeReveal, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";

type Profile = {
  id: string;
  stay_id: string;
  origin_lifecycle_version: number;
  external_reservation_id: string;
  normalized_room_number: string | null;
  pms_interface_label?: string;
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
  // Explain why an active profile cannot authenticate; guests receive no such distinction.
  return { label: "Active, not usable", tone: "warn", hint: "Expired, or the stay moved on" };
}

/** Wait for role loading before showing a read-only notice. */
export function PostStayView({ canAct, rolesKnown = true }: { canAct: boolean; rolesKnown?: boolean }) {
  const toast = useToast();
  const { multi } = usePmsConnections();
  const [rows, setRows] = useState<Profile[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<{ kind: "reset" | "revoke"; row: Profile } | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("");
  const [confirmWord, setConfirmWord] = useState("");
  // Reset changes only the PIN; extending validity requires a separate choice.
  const [extendValidity, setExtendValidity] = useState(false);
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
        // Keep the last good list when refresh fails.
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

  // Require a typed confirmation because revocation cannot be undone.
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
        ...(dialog.kind === "reset" && extendValidity ? { extend_validity: true } : {}),
      });
      if (dialog.kind === "reset" && res.pin) {
        setRevealed({ profileId: dialog.row.id, pin: res.pin, validUntil: res.valid_until ?? "" });
      } else if (dialog.kind === "revoke") {
        toast.success("Post-stay access ended", `Room ${dialog.row.normalized_room_number ?? "—"}${multi && dialog.row.pms_interface_label ? ` at ${dialog.row.pms_interface_label}` : ""} can no longer reconnect with a PIN.`);
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

  return (
    <PageShell>
      <PageHeader
        title="Post-stay access"
        description="Reset a lost post-stay PIN, or end a guest's post-stay access."
        help={
          <>
            <HelpSection title="What post-stay access is">
              <p>
                After checkout a guest can reconnect with a PIN for a limited time. A PIN belongs to one stay, never
                to a room: when the room is re-let, the previous PIN stops working on its own.
              </p>
            </HelpSection>
            <HelpSection title="Two different actions">
              <HelpList items={[
                <><strong>Reset PIN</strong> gives the guest a new PIN. They keep their post-stay access; only the secret changes. Use it when a guest lost the PIN or never received it.</>,
                <><strong>End access</strong> ends post-stay access for that stay. It cannot be undone, and the guest gets a new post-stay PIN only after a new stay.</>,
              ]} />
            </HelpSection>
            <HelpSection title="Why there is no &ldquo;show PIN&rdquo;">
              <p>
                The PIN itself is never stored, so it cannot be shown again. The only time a PIN appears is once,
                right after a reset.
              </p>
            </HelpSection>
          </>
        }
      />

      {rolesKnown && !canAct && <ReadOnlyNotice />}
      <ErrorBanner err={error} />

      <StatGrid cols={3}>
        <StatCard label="Can reconnect now" value={rows ? counts.usable.toLocaleString() : "—"} />
        <StatCard
          label="Active, not usable"
          value={rows ? counts.notUsable.toLocaleString() : "—"}
          hint="Expired, or the stay moved on"
        />
        <StatCard label="Ended by staff" value={rows ? counts.ended.toLocaleString() : "—"} />
      </StatGrid>

      <Card className="overflow-hidden">
        {rows === null ? (
          <SkeletonRows rows={4} cols={6} />
        ) : rows.length === 0 ? (
          <EmptyState size="sm"
            icon={<CalendarClock />}
            title="No post-stay access yet"
            hint="A departing guest given post-stay access appears here with their room and PIN validity."
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
                <TH className="hidden sm:table-cell">Valid until</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((row) => {
                const st = stateOf(row);
                return (
                  <TR key={row.id} onClick={() => setDetail(row)}>
                    <TD nowrap className="font-medium">
                      {row.normalized_room_number ? `Room ${row.normalized_room_number}` : "No room"}
                      {multi && row.pms_interface_label && <div className="text-xs font-normal text-muted-foreground">{row.pms_interface_label}</div>}
                    </TD>
                    <TD truncate title={row.external_reservation_id || undefined} className="hidden max-w-[12rem] md:table-cell">{row.external_reservation_id || "—"}</TD>
                    <TD muted className="hidden lg:table-cell">
                      {stayWords(row.stay_status)}
                      <div className="text-xs">Stay episode {row.origin_lifecycle_version}</div>
                    </TD>
                    <TD nowrap>
                      <StatusBadge tone={st.tone}>{st.label}</StatusBadge>
                      {st.hint && <div className="mt-0.5 text-xs text-muted-foreground">{st.hint}</div>}
                    </TD>
                    <TD nowrap muted className="hidden tabular sm:table-cell">
                      {row.pin_generation === 1 ? "First PIN" : `Reset ${row.pin_generation - 1}×`}
                    </TD>
                    <TD nowrap muted className="hidden sm:table-cell">{formatDate(row.valid_until)}</TD>
                    <TD className="w-0">
                      {canAct && row.status === "ACTIVE" ? (
                        <RowActions
                          subject={row.normalized_room_number ? `room ${row.normalized_room_number}` : "no room"}
                          primary={{ label: "Reset PIN", onSelect: () => setDialog({ kind: "reset", row }) }}
                          items={[
                            { label: "Details", onSelect: () => setDetail(row) },
                            { label: "End access", danger: true, onSelect: () => setDialog({ kind: "revoke", row }) },
                          ]}
                        />
                      ) : (
                        <RowActions
                          subject={row.normalized_room_number ? `room ${row.normalized_room_number}` : "no room"}
                          primary={{ label: "Details", onSelect: () => setDetail(row) }}
                        />
                      )}
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>

      <DetailsDrawer
        open={detail !== null}
        onOpenChange={(v) => !v && setDetail(null)}
        eyebrow="Post-stay access"
        title={detail?.normalized_room_number ? `Room ${detail.normalized_room_number}${multi && detail.pms_interface_label ? ` · ${detail.pms_interface_label}` : ""}` : "Post-stay access"}
        description={detail ? `Reservation ${detail.external_reservation_id || "—"}` : undefined}
        badges={detail ? (
          <>
            <StatusBadge tone={stateOf(detail).tone}>{stateOf(detail).label}</StatusBadge>
            <Badge tone="neutral">{stayWords(detail.stay_status)}</Badge>
          </>
        ) : undefined}
      >
        {detail && (
          <>
            {detail.status === "REVOKED" && (
              <Callout tone="neutral" title="Access was ended for this stay">
                {detail.revoke_reason ? <>Reason recorded: {detail.revoke_reason}</> : "No reason was recorded."}
              </Callout>
            )}
            <DescriptionList
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
      </DetailsDrawer>

      <DialogForm
        open={dialog !== null}
        onOpenChange={(v) => { if (!v) closeDialog(); }}
        title={dialog?.kind === "revoke" ? "End post-stay access" : "Reset the PIN"}
        description={
          dialog?.kind === "revoke"
            ? `Room ${dialog.row.normalized_room_number ?? "—"} · reservation ${dialog.row.external_reservation_id}`
            : "A new PIN replaces the old one immediately. The guest keeps their post-stay access and its validity; only the secret changes. The new PIN is shown once, on this screen."
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
        {dialog?.kind === "reset" && (
          <Checkbox
            checked={extendValidity}
            onChange={(e) => setExtendValidity(e.target.checked)}
            label="Also restart the validity"
            description={`The new PIN works for 24 hours from now. Leave this off to keep it valid until ${formatDate(dialog.row.valid_until)}.`}
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
