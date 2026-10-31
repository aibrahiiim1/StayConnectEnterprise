"use client";

// The reconnections list of After Check-out Access: who reconnected with room + reservation number, and staff ending a
// stay's reconnection, irreversibly. It sets no duration or package; those live in the After Check-out Access policy.

import { usePmsConnections } from "@/lib/pms-scope";
import * as React from "react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { CalendarClock } from "lucide-react";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm } from "@/components/ui/dialog";
import { DetailsDrawer } from "@/components/ui/details-drawer";
import { RowActions } from "@/components/ui/row-actions";
import { DescriptionList } from "@/components/ui/data";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { ConsequenceList, ReadOnlyNotice } from "@/components/ui/patterns";
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
  issued_via: string;
  issued_at: string;
  valid_until: string;
  revoked_at: string | null;
  revoke_reason: string | null;
  authenticable: boolean;
};

const STAY_WORDS: Record<string, string> = {
  IN_HOUSE: "In house",
  RESERVED: "Arriving",
  CHECKED_OUT: "Checked out",
  POST_STAY_ACTIVE: "Reconnection period",
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

function Shell({ embedded, children }: { embedded: boolean; children: React.ReactNode }) {
  return embedded ? <section aria-label="Reconnections" className="space-y-5">{children}</section> : <PageShell>{children}</PageShell>;
}

/** Wait for role loading before showing a read-only notice. */
export function PostStayView({ canAct, rolesKnown = true, embedded = false }: { canAct: boolean; rolesKnown?: boolean; embedded?: boolean }) {
  const toast = useToast();
  const { multi } = usePmsConnections();
  const [rows, setRows] = useState<Profile[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<{ kind: "revoke"; row: Profile } | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("");
  const [confirmWord, setConfirmWord] = useState("");
  const [detail, setDetail] = useState<Profile | null>(null);
  const [unavailable, setUnavailable] = useState(false);

  const load = useCallback(() => {
    api
      .get<{ profiles?: Profile[] }>("/post-stay-profiles/")
      .then((m) => {
        setRows(m.profiles ?? []);
        setError(null);
      })
      .catch((e) => {
        if (embedded && (e?.status === 404 || e?.status === 403)) setUnavailable(true);
        setError(String(e?.message ?? e));
        // Keep the last good list when refresh fails.
        setRows((prev) => prev ?? []);
      });
  }, [embedded]);

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
    if (confirmWord.trim().toUpperCase() !== "REVOKE") return false;
    return true;
  }, [dialog, password, reason, confirmWord]);

  async function submit() {
    if (!dialog || !canSubmit) return;
    setBusy(true);
    setDialogError(null);
    try {
      await api.post(`/post-stay-profiles/${dialog.row.id}/revoke`, { password, reason: reason.trim() });
      toast.success("Reconnection ended", `Room ${dialog.row.normalized_room_number ?? "—"}${multi && dialog.row.pms_interface_label ? ` at ${dialog.row.pms_interface_label}` : ""} can no longer reconnect after check-out.`);
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

  if (unavailable) return null;

  return (
    <Shell embedded={embedded}>
      {embedded ? (
        <div className="space-y-1">
          <h2 className="text-lg font-semibold">Reconnections</h2>
          <p className="text-sm text-muted-foreground">
            Stays whose guests reconnected after check-out with the room number and reservation number. Ending one
            stops further reconnection for that stay; it cannot be undone.
          </p>
        </div>
      ) : (
        <PageHeader
          title="Reconnections after check-out"
          description="See who reconnected after check-out, or end a stay's reconnection."
        />
      )}

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
            title="No reconnections yet"
            hint="A guest who has reconnected after check-out appears here with their room and until when."
          />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Room</TH>
                <TH className="hidden md:table-cell">Reservation</TH>
                <TH className="hidden lg:table-cell">Stay</TH>
                <TH>State</TH>
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
                    <TD nowrap muted className="hidden sm:table-cell">{formatDate(row.valid_until)}</TD>
                    <TD className="w-0">
                      {canAct && row.status === "ACTIVE" ? (
                        <RowActions
                          subject={row.normalized_room_number ? `room ${row.normalized_room_number}` : "no room"}
                          primary={{ label: "Details", onSelect: () => setDetail(row) }}
                          items={[
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
        eyebrow="Reconnection"
        title={detail?.normalized_room_number ? `Room ${detail.normalized_room_number}${multi && detail.pms_interface_label ? ` · ${detail.pms_interface_label}` : ""}` : "Reconnection"}
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
                { label: "Issued", value: formatDate(detail.issued_at) },
                { label: "Issued via", value: detail.issued_via ? detail.issued_via.replace(/_/g, " ").toLowerCase() : "—" },
                { label: "Valid until", value: formatDate(detail.valid_until) },
                { label: "Ended", value: detail.revoked_at ? formatDate(detail.revoked_at) : "—" },
                { label: "Stay reference", value: <MonoId value={detail.stay_id} title="Stay" /> },
                { label: "Access reference", value: <MonoId value={detail.id} title="Reconnection" /> },
              ]}
            />
          </>
        )}
      </DetailsDrawer>

      <DialogForm
        open={dialog !== null}
        onOpenChange={(v) => { if (!v) closeDialog(); }}
        title="End reconnection"
        description={dialog ? `Room ${dialog.row.normalized_room_number ?? "—"} · reservation ${dialog.row.external_reservation_id}` : undefined}
        size="sm"
        submitLabel="End access permanently"
        submitVariant="danger"
        busy={busy}
        busyLabel="Ending…"
        error={dialogError}
        disabled={!canSubmit}
        onSubmit={submit}
      >
        {dialog?.kind === "revoke" && (
          <ConsequenceList
            items={[
              `Reconnection ends for this stay (episode ${dialog.row.origin_lifecycle_version}).`,
              "This stay cannot be used to reconnect again.",
            ]}
          />
        )}
        <Field label="Reason" required hint="Recorded in the activity log. At least 4 characters.">
          <Input
            value={reason}
            maxLength={500}
            onChange={(e) => setReason(e.target.value)}
            placeholder="Guest asked us to end it"
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

    </Shell>
  );
}
