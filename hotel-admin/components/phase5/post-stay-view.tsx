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
// response — which is why the reveal panel says so and cannot be reopened.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";

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

export function PostStayView({ canAct }: { canAct: boolean }) {
  const [rows, setRows] = useState<Profile[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<{ kind: "reset" | "revoke"; row: Profile } | null>(null);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("");
  const [confirmWord, setConfirmWord] = useState("");
  const [revealed, setRevealed] = useState<Revealed | null>(null);

  const load = useCallback(() => {
    api
      .get<{ profiles?: Profile[] }>("/post-stay-profiles/")
      .then((m) => {
        setRows(m.profiles ?? []);
        setError(null);
      })
      .catch((e) => setError(String(e?.message ?? e)));
  }, []);

  useEffect(load, [load]);

  const closeDialog = () => {
    setDialog(null);
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
    setError(null);
    try {
      const path = `/post-stay-profiles/${dialog.row.id}/${dialog.kind}`;
      const res = await api.post<{ pin?: string; valid_until?: string }>(path, {
        password,
        reason: reason.trim(),
      });
      if (dialog.kind === "reset" && res.pin) {
        setRevealed({ profileId: dialog.row.id, pin: res.pin, validUntil: res.valid_until ?? "" });
      }
      closeDialog();
      load();
    } catch (e: unknown) {
      setError(String((e as { message?: string })?.message ?? e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-6">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold">Post-stay access</h1>
        <p className="text-sm text-muted-foreground">
          A post-stay PIN belongs to one stay episode, never to a room. When a guest is reinstated or the room
          is re-let, the previous PIN stops working on its own — nothing has to be revoked for that to happen.
        </p>
      </header>

      {error && (
        <div role="alert" className="rounded border border-red-300 bg-red-50 p-3 text-sm text-red-800">
          {error}
        </div>
      )}

      {revealed && (
        // The one-time reveal. It is shown after a reset and cannot be reopened: the appliance never stored
        // this value and cannot produce it again.
        <div
          role="status"
          className="rounded border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900 space-y-2"
        >
          <div className="font-semibold">New PIN — shown once</div>
          <div className="font-mono text-2xl tracking-widest">{revealed.pin}</div>
          <p>
            Give this to the guest now. It is not stored and cannot be shown again — closing this panel loses
            it. If that happens, reset again; the guest keeps their access either way.
          </p>
          {revealed.validUntil && <p>Valid until {revealed.validUntil}</p>}
          <button
            type="button"
            className="rounded border border-amber-400 px-3 py-1"
            onClick={() => setRevealed(null)}
          >
            I have given it to the guest
          </button>
        </div>
      )}

      <table className="w-full text-sm">
        <thead className="text-left text-muted-foreground">
          <tr>
            <th className="py-2">Reservation</th>
            <th>Room</th>
            <th>Stay</th>
            <th>Episode</th>
            <th>State</th>
            <th>PIN generation</th>
            <th>Valid until</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {rows === null && (
            <tr>
              <td colSpan={8} className="py-4 text-muted-foreground">
                Loading…
              </td>
            </tr>
          )}
          {rows?.length === 0 && (
            <tr>
              <td colSpan={8} className="py-4 text-muted-foreground">
                No post-stay identities yet.
              </td>
            </tr>
          )}
          {rows?.map((row) => (
            <tr key={row.id} className="border-t">
              <td className="py-2">{row.external_reservation_id}</td>
              <td>{row.normalized_room_number ?? "—"}</td>
              <td>{row.stay_status}</td>
              <td>{row.origin_lifecycle_version}</td>
              <td>
                {row.status === "REVOKED" ? (
                  <span title={row.revoke_reason ?? undefined}>Revoked — ended for this stay</span>
                ) : row.authenticable ? (
                  <span>Active</span>
                ) : (
                  // ACTIVE but not authenticable: expired, or the stay moved to a new episode. Saying which
                  // matters here — this is the operator's screen, and the guest sees nothing either way.
                  <span>Active, not usable (expired or the stay moved on)</span>
                )}
              </td>
              <td>{row.pin_generation}</td>
              <td>{row.valid_until}</td>
              <td className="space-x-2 text-right">
                <button
                  type="button"
                  disabled={!canAct || row.status !== "ACTIVE"}
                  className="rounded border px-2 py-1 disabled:opacity-40"
                  onClick={() => setDialog({ kind: "reset", row })}
                >
                  Reset PIN
                </button>
                <button
                  type="button"
                  disabled={!canAct || row.status !== "ACTIVE"}
                  className="rounded border border-red-400 px-2 py-1 text-red-700 disabled:opacity-40"
                  onClick={() => setDialog({ kind: "revoke", row })}
                >
                  End access
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      {dialog && (
        <div role="dialog" aria-modal="true" className="rounded border p-4 space-y-3">
          <h2 className="text-lg font-semibold">
            {dialog.kind === "reset" ? "Reset the PIN" : "End post-stay access"}
          </h2>
          <p className="text-sm text-muted-foreground">
            {dialog.kind === "reset" ? (
              <>
                A new PIN replaces the old one immediately. The guest keeps their post-stay access; only the
                secret changes. The new PIN is shown once, on this screen.
              </>
            ) : (
              <>
                This ends post-stay access for reservation {dialog.row.external_reservation_id} (stay episode{" "}
                {dialog.row.origin_lifecycle_version}). <strong>It cannot be undone</strong>, and this stay
                gets no replacement PIN. If the guest only lost their PIN, reset it instead.
              </>
            )}
          </p>
          <label className="block text-sm">
            Reason (recorded in the audit log)
            <input
              className="mt-1 w-full rounded border px-2 py-1"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={dialog.kind === "reset" ? "Guest lost the printout" : "Guest asked us to end it"}
            />
          </label>
          <label className="block text-sm">
            Your password
            <input
              type="password"
              className="mt-1 w-full rounded border px-2 py-1"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          {dialog.kind === "revoke" && (
            <label className="block text-sm">
              Type REVOKE to confirm
              <input
                className="mt-1 w-full rounded border px-2 py-1"
                value={confirmWord}
                onChange={(e) => setConfirmWord(e.target.value)}
              />
            </label>
          )}
          <div className="flex gap-2">
            <button
              type="button"
              disabled={!canSubmit || busy}
              className={
                dialog.kind === "revoke"
                  ? "rounded bg-red-600 px-3 py-1 text-white disabled:opacity-40"
                  : "rounded bg-slate-800 px-3 py-1 text-white disabled:opacity-40"
              }
              onClick={submit}
            >
              {dialog.kind === "reset" ? "Reset PIN" : "End access permanently"}
            </button>
            <button type="button" className="rounded border px-3 py-1" onClick={closeDialog}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
