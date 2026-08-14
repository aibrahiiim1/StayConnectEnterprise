"use client";

// THE CROSS-PMS TRANSFER SCREEN (Phase 5, DARK).
//
// A transfer ENDS a guest's access on one property and re-establishes it on another. The screen is built so
// that the operator sees what that means before they can do it:
//
//   * the review signals are shown FIRST and labelled as signals. They are ambiguous authentication
//     outcomes, not transfers, and the panel says so in the words the API returns. There is deliberately no
//     "transfer this one" button anywhere near them — an operator who could act directly from a signal would
//     be acting on an inference nobody made.
//   * the operator must PREVIEW before the confirm controls appear at all. The preview names both rooms and
//     counts the devices and sessions about to move, so the confirmation is a decision rather than a
//     sentence.
//   * a blocker is shown as plain language before the operator types anything. "This is a room move, not a
//     transfer" costs one screen instead of a completed dialog and a rejected submission.

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";

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

export function StayTransferView({ canAct }: { canAct: boolean }) {
  const [signals, setSignals] = useState<Signal[] | null>(null);
  const [signalNotice, setSignalNotice] = useState("");
  const [rows, setRows] = useState<TransferRow[] | null>(null);
  const [fromStay, setFromStay] = useState("");
  const [toStay, setToStay] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
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
    setDone(null);
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

  async function execute() {
    if (!preview || preview.blocker || reason.trim().length < 4 || !password) return;
    setBusy(true);
    setError(null);
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
      setDone(
        `Access moved. ${res.sessions_rebound ?? 0} session(s) stayed connected through the change.`,
      );
      setPreview(null);
      setPassword("");
      setReason("");
      setFromStay("");
      setToStay("");
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
        <h1 className="text-2xl font-semibold">Cross-PMS transfer</h1>
        <p className="text-sm text-muted-foreground">
          Moves a guest&apos;s live access from a stay on one PMS interface to a stay on another. This is not a
          room move: a guest changing rooms on the same interface keeps their access automatically and needs
          nothing here.
        </p>
      </header>

      {error && (
        <div role="alert" className="rounded border border-red-300 bg-red-50 p-3 text-sm text-red-800">
          {error}
        </div>
      )}
      {done && (
        <div role="status" className="rounded border border-green-300 bg-green-50 p-3 text-sm text-green-900">
          {done}
        </div>
      )}

      <section className="space-y-2" data-testid="review-signals">
        <h2 className="text-lg font-semibold">Review signals</h2>
        {/* The API's own words, rendered rather than paraphrased: a screen that softened them would be the
            place the "ambiguity means the guest moved" habit starts. */}
        <p className="text-sm text-muted-foreground" data-testid="signal-notice">
          {signalNotice ||
            "Ambiguous authentication outcomes. They are not transfers and are never evidence that a guest moved."}
        </p>
        {signals?.length === 0 && <p className="text-sm">No ambiguous resolutions in the last 7 days.</p>}
        {!!signals?.length && (
          <table className="w-full text-sm">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="py-2">Outcome</th>
                <th>Guest network</th>
                <th>Occurrences</th>
                <th>Most recent</th>
              </tr>
            </thead>
            <tbody>
              {signals.map((s) => (
                <tr key={`${s.outcome_code}-${s.guest_network_id}`} className="border-t">
                  <td className="py-2">{s.outcome_code}</td>
                  <td>{s.guest_network_id}</td>
                  <td>{s.occurrences}</td>
                  <td>{s.resolved_at}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="space-y-3 rounded border p-4">
        <h2 className="text-lg font-semibold">Transfer a guest</h2>
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="block text-sm">
            From stay
            <input
              className="mt-1 w-full rounded border px-2 py-1"
              value={fromStay}
              onChange={(e) => setFromStay(e.target.value)}
              placeholder="Stay id on the origin PMS"
            />
          </label>
          <label className="block text-sm">
            To stay
            <input
              className="mt-1 w-full rounded border px-2 py-1"
              value={toStay}
              onChange={(e) => setToStay(e.target.value)}
              placeholder="Stay id on the destination PMS"
            />
          </label>
        </div>
        <button
          type="button"
          className="rounded border px-3 py-1 disabled:opacity-40"
          disabled={!canAct || busy}
          onClick={runPreview}
        >
          Preview
        </button>

        {preview && preview.blocker && (
          <div role="alert" className="rounded border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900">
            <strong>This transfer cannot be performed.</strong> {preview.blocker}
          </div>
        )}

        {preview && !preview.blocker && (
          <div className="space-y-3 rounded border border-slate-300 p-3 text-sm">
            <div data-testid="preview-summary">
              Moving reservation <strong>{preview.from_external_reservation_id}</strong> (room{" "}
              {preview.from_room || "—"}) to <strong>{preview.to_external_reservation_id}</strong> (room{" "}
              {preview.to_room || "—"}).
            </div>
            <div>
              {preview.live_devices} device(s) and {preview.live_sessions} live session(s) will move. The guest
              stays connected: no logout, no re-authentication.
            </div>
            <div className="text-muted-foreground">
              The access on the origin stay ends when this completes. It is not returned automatically if the
              guest goes back.
            </div>
            <label className="block">
              Reason (recorded in the audit log)
              <input
                className="mt-1 w-full rounded border px-2 py-1"
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder="Guest moved to the sister property"
              />
            </label>
            <label className="block">
              Your password
              <input
                type="password"
                className="mt-1 w-full rounded border px-2 py-1"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
            <button
              type="button"
              className="rounded bg-slate-800 px-3 py-1 text-white disabled:opacity-40"
              disabled={!canAct || busy || reason.trim().length < 4 || !password}
              onClick={execute}
            >
              Transfer access
            </button>
          </div>
        )}
      </section>

      <section className="space-y-2">
        <h2 className="text-lg font-semibold">Recorded transfers</h2>
        {rows?.length === 0 && <p className="text-sm text-muted-foreground">No transfers recorded.</p>}
        {!!rows?.length && (
          <table className="w-full text-sm">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="py-2">From</th>
                <th>To</th>
                <th>When</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id} className="border-t">
                  <td className="py-2">
                    {r.from_external_reservation_id} (room {r.from_room || "—"})
                  </td>
                  <td>
                    {r.to_external_reservation_id} (room {r.to_room || "—"})
                  </td>
                  <td>{r.created_at}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}
