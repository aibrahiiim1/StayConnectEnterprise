"use client";

// THE VOUCHER SCREEN.
//
// Until this existed, nothing in the product could issue a voucher. scd had the route; no caller anywhere
// reached it. So VOUCHER was an authentication method a property could switch on, which then refused every
// guest — correctly, because there were no vouchers and no way to make one.
//
// FOUR JOBS, AND THE SCREEN'S WORK IS KEEPING THEM APART:
//
//   ISSUE    prints a batch. The codes appear once, here, in the response.
//   REVEAL   recovers ONE code for a card already in circulation. Password, a reason, a permanent record.
//   EXPORT   recovers a whole batch under the same gate, with one record naming the size of the selection.
//   REVOKE   cancels an UNUSED card. It cannot cancel a redeemed one: that guest already has access, and
//            taking access back is an entitlement action on a different screen.
//
// THE HONEST SENTENCE ABOUT CONFIDENTIALITY, which this screen has to say out loud rather than imply.
// Unlike a post-stay PIN or a guest password — both hashed — a voucher code is encrypted and CAN be read
// again. So the reveal dialog does not promise "this is the only time you will see this": that would be
// false. It says who will see that you looked, which is true, and which is the thing that actually
// constrains what people do.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import type { Voucher, VoucherCodeFormat, VoucherReveal, VoucherState } from "@/lib/api";

// The one-time response of a print run. It exists only here: nothing stores a plaintext code.
type IssuedBatch = { batch_id: string; count: number; codes: string[] };

const STATE_WORDS: Record<VoucherState, string> = {
  UNUSED: "Not used yet",
  REDEEMED: "Redeemed",
  REVOKED: "Cancelled",
  REDEMPTION_EXPIRED: "Expired unused",
};

export function VouchersView(props: {
  canIssue: boolean;
  canRevealCodes: boolean;
  canEditFormat: boolean;
}) {
  const { canIssue, canRevealCodes, canEditFormat } = props;

  const [rows, setRows] = useState<Voucher[] | null>(null);
  const [reveals, setReveals] = useState<VoucherReveal[] | null>(null);
  const [format, setFormat] = useState<VoucherCodeFormat | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [stateFilter, setStateFilter] = useState("");

  // Issue form
  const [revisionId, setRevisionId] = useState("");
  const [count, setCount] = useState(10);
  const [note, setNote] = useState("");
  const [validUntil, setValidUntil] = useState("");
  const [issued, setIssued] = useState<IssuedBatch | null>(null);

  // The step-up dialogs
  const [dialog, setDialog] = useState<
    | { kind: "reveal"; row: Voucher }
    | { kind: "revoke"; row: Voucher }
    | { kind: "export"; batch: string; size: number }
    | null
  >(null);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("");
  const [revealed, setRevealed] = useState<{ id: string; code: string } | null>(null);
  const [exported, setExported] = useState<{ batch: string; rows: { id: string; code: string }[] } | null>(null);

  const load = useCallback(() => {
    const q = stateFilter ? `?state=${encodeURIComponent(stateFilter)}` : "";
    api
      .get<{ vouchers?: Voucher[] }>(`/vouchers/${q}`)
      .then((m) => {
        setRows(m.vouchers ?? []);
        setError(null);
      })
      .catch((e) => setError(String(e?.message ?? e)));
  }, [stateFilter]);

  const loadReveals = useCallback(() => {
    if (!canRevealCodes) return;
    api
      .get<{ reveals?: VoucherReveal[] }>("/voucher-codes/reveals")
      .then((m) => setReveals(m.reveals ?? []))
      .catch(() => setReveals(null));
  }, [canRevealCodes]);

  useEffect(load, [load]);
  useEffect(loadReveals, [loadReveals]);
  useEffect(() => {
    api.get<VoucherCodeFormat>("/voucher-code-settings/").then(setFormat).catch(() => setFormat(null));
  }, []);

  // Batches, derived from the rows rather than fetched: a batch is a grouping of vouchers, not a record of
  // its own, and inventing a second source for it is how the two disagree.
  const batches = useMemo(() => {
    const seen = new Map<string, number>();
    for (const r of rows ?? []) if (r.batch_id) seen.set(r.batch_id, (seen.get(r.batch_id) ?? 0) + 1);
    return [...seen.entries()].sort((a, b) => b[1] - a[1]);
  }, [rows]);

  const closeDialog = () => {
    setDialog(null);
    setPassword("");
    setReason("");
  };

  const canSubmit = password.length > 0 && reason.trim().length >= 4 && !busy;

  async function submit() {
    if (!dialog) return;
    setBusy(true);
    setError(null);
    try {
      if (dialog.kind === "reveal") {
        const out = await api.post<{ voucher_id: string; code: string }>(
          `/voucher-codes/${dialog.row.id}/reveal`,
          { password, reason: reason.trim() },
        );
        setRevealed({ id: out.voucher_id, code: out.code });
      } else if (dialog.kind === "revoke") {
        await api.post(`/vouchers/${dialog.row.id}/revoke`, { password, reason: reason.trim() });
        load();
      } else {
        const out = await api.post<{ batch_id: string; vouchers: { id: string; code: string }[] }>(
          "/voucher-codes/export",
          { password, reason: reason.trim(), batch_id: dialog.batch },
        );
        setExported({ batch: out.batch_id, rows: out.vouchers });
      }
      closeDialog();
      loadReveals();
    } catch (e: any) {
      setError(String(e?.message ?? e));
    } finally {
      setBusy(false);
    }
  }

  async function issue() {
    setBusy(true);
    setError(null);
    try {
      const body: Record<string, unknown> = {
        package_revision_id: revisionId.trim(),
        count,
      };
      if (note.trim()) body.note = note.trim();
      if (validUntil) body.valid_until = new Date(validUntil).toISOString();
      const out = await api.post<IssuedBatch>("/vouchers/issue", body);
      setIssued(out);
      load();
    } catch (e: any) {
      setError(String(e?.message ?? e));
    } finally {
      setBusy(false);
    }
  }

  async function saveFormat(mode: "numbers" | "mixed", length: number) {
    setBusy(true);
    setError(null);
    try {
      const out = await api.put<VoucherCodeFormat>("/voucher-code-settings/", {
        code_mode: mode,
        code_length: length,
      });
      setFormat(out);
    } catch (e: any) {
      setError(String(e?.message ?? e));
    } finally {
      setBusy(false);
    }
  }

  // CSV is built here rather than asked of the server, because the server has already done the part only it
  // can do — opening the codes, and recording that it did. Turning rows into a file is not a second
  // privileged act and does not deserve a second audit row.
  function downloadExported() {
    if (!exported) return;
    const csv = ["voucher_id,code", ...exported.rows.map((r) => `${r.id},${r.code}`)].join("\n");
    const blob = new Blob([csv], { type: "text/csv" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `vouchers-${exported.batch.slice(0, 8)}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  }

  return (
    <div className="space-y-6 p-6">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold">Vouchers</h1>
        <p className="text-sm text-muted-foreground">
          Printed cards a guest redeems for internet access. A code is encrypted and can be read again —
          every time it is, this screen records who read it and why.
        </p>
      </header>

      {error && (
        <div role="alert" className="rounded-md border border-destructive/30 bg-destructive-subtle p-3 text-sm">
          {error}
        </div>
      )}

      {/* ---- the code format (migration 0085) ---- */}
      <section className="rounded-md border p-4 space-y-3">
        <h2 className="font-semibold">Code format</h2>
        {format === null ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : (
          <>
            <p className="text-sm text-muted-foreground">
              {format.config_version === 0
                ? "Nobody has chosen a format, so new batches use the defaults below."
                : `Chosen at this property (version ${format.config_version}).`}{" "}
              Digits only suits a numeric keypad; mixed suits a printed card. Never more than eight
              characters. Changing this affects the <strong>next</strong> batch you print — cards already
              issued keep the format they were printed with and stay redeemable.
            </p>
            <div className="flex flex-wrap items-end gap-3 text-sm">
              <label className="block">
                Characters
                <select
                  className="mt-1 block rounded border px-2 py-1"
                  value={format.code_mode}
                  disabled={!canEditFormat || busy}
                  onChange={(e) => saveFormat(e.target.value as "numbers" | "mixed", format.code_length)}
                >
                  <option value="numbers">Digits only</option>
                  <option value="mixed">Digits and letters</option>
                </select>
              </label>
              <label className="block">
                Length
                <select
                  className="mt-1 block rounded border px-2 py-1"
                  value={format.code_length}
                  disabled={!canEditFormat || busy}
                  onChange={(e) => saveFormat(format.code_mode, Number(e.target.value))}
                >
                  {[6, 7, 8].map((n) => (
                    <option key={n} value={n}>
                      {n} characters
                    </option>
                  ))}
                </select>
              </label>
              {!canEditFormat && (
                <span className="text-muted-foreground">
                  Your role can see the format and not change it.
                </span>
              )}
            </div>
          </>
        )}
      </section>

      {/* ---- issue ---- */}
      {canIssue && (
        <section className="rounded-md border p-4 space-y-3">
          <h2 className="font-semibold">Print a batch</h2>
          <div className="flex flex-wrap items-end gap-3 text-sm">
            <label className="block">
              Internet package revision
              <input
                className="mt-1 block w-80 rounded border px-2 py-1"
                value={revisionId}
                onChange={(e) => setRevisionId(e.target.value)}
                placeholder="the revision these cards grant"
              />
            </label>
            <label className="block">
              How many
              <input
                type="number"
                min={1}
                max={500}
                className="mt-1 block w-24 rounded border px-2 py-1"
                value={count}
                onChange={(e) => setCount(Number(e.target.value))}
              />
            </label>
            <label className="block">
              Valid until (optional)
              <input
                type="datetime-local"
                className="mt-1 block rounded border px-2 py-1"
                value={validUntil}
                onChange={(e) => setValidUntil(e.target.value)}
              />
            </label>
            <label className="block">
              Note (optional)
              <input
                className="mt-1 block w-64 rounded border px-2 py-1"
                value={note}
                onChange={(e) => setNote(e.target.value)}
                placeholder="conference desk, week 12"
              />
            </label>
            <button
              type="button"
              disabled={busy || !revisionId.trim() || count < 1 || count > 500}
              className="rounded-md bg-primary px-3 py-1 font-medium text-primary-foreground disabled:opacity-50"
              onClick={issue}
            >
              Print {count}
            </button>
          </div>
          <p className="text-xs text-muted-foreground">
            Leave <em>Valid until</em> empty for cards that never expire. A date already past is refused —
            cards printed from it could never be redeemed.
          </p>
        </section>
      )}

      {/* ---- the one-time batch sheet ---- */}
      {issued && (
        <section
          role="status"
          className="rounded-md border border-warning/30 bg-warning-subtle p-4 text-sm space-y-2"
        >
          <div className="font-semibold">
            {issued.count} codes — printed now, shown here
          </div>
          <p>
            Print or copy these now. Closing this panel does not destroy them: unlike a post-stay PIN, a
            voucher code can be recovered later — but doing so is recorded against your name, so it is
            easier to keep them now.
          </p>
          <div className="max-h-60 overflow-y-auto rounded bg-background p-2 font-mono text-base tracking-widest">
            {issued.codes.map((c) => (
              <div key={c}>{c}</div>
            ))}
          </div>
          <div className="flex gap-2">
            <button type="button" className="rounded border px-3 py-1" onClick={() => window.print()}>
              Print
            </button>
            <button type="button" className="rounded border px-3 py-1" onClick={() => setIssued(null)}>
              Done
            </button>
          </div>
        </section>
      )}

      {/* ---- a single revealed code ---- */}
      {revealed && (
        <section role="status" className="rounded-md border border-warning/30 bg-warning-subtle p-4 text-sm space-y-2">
          <div className="font-semibold">Code for card {revealed.id.slice(0, 8)}</div>
          <div className="font-mono text-2xl tracking-widest">{revealed.code}</div>
          <p>This reveal is recorded with your name and your reason. It cannot be un-recorded.</p>
          <button type="button" className="rounded border px-3 py-1" onClick={() => setRevealed(null)}>
            Close
          </button>
        </section>
      )}

      {/* ---- an exported batch ---- */}
      {exported && (
        <section role="status" className="rounded-md border border-warning/30 bg-warning-subtle p-4 text-sm space-y-2">
          <div className="font-semibold">
            {exported.rows.length} codes from batch {exported.batch.slice(0, 8)}
          </div>
          <p>One record was written naming you, your reason and the size of this selection.</p>
          <div className="flex gap-2">
            <button type="button" className="rounded border px-3 py-1" onClick={downloadExported}>
              Download CSV
            </button>
            <button type="button" className="rounded border px-3 py-1" onClick={() => setExported(null)}>
              Close
            </button>
          </div>
        </section>
      )}

      {/* ---- batches ---- */}
      {batches.length > 0 && canRevealCodes && (
        <section className="rounded-md border p-4 space-y-2">
          <h2 className="font-semibold">Batches</h2>
          <ul className="space-y-1 text-sm">
            {batches.map(([id, n]) => (
              <li key={id} className="flex items-center gap-3">
                <span className="font-mono">{id.slice(0, 8)}</span>
                <span className="text-muted-foreground">{n} cards in view</span>
                <button
                  type="button"
                  className="rounded border px-2 py-0.5"
                  onClick={() => setDialog({ kind: "export", batch: id, size: n })}
                >
                  Export codes
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}

      {/* ---- the list ---- */}
      <section className="space-y-2">
        <div className="flex items-center gap-3">
          <h2 className="font-semibold">Cards</h2>
          <select
            className="rounded border px-2 py-1 text-sm"
            value={stateFilter}
            onChange={(e) => setStateFilter(e.target.value)}
          >
            <option value="">All states</option>
            {Object.entries(STATE_WORDS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        <table className="w-full text-sm">
          <thead className="text-left text-muted-foreground">
            <tr>
              <th className="py-2">Card</th>
              <th>State</th>
              <th>Batch</th>
              <th>Printed</th>
              <th>Valid until</th>
              <th>Note</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {rows === null && (
              <tr>
                <td colSpan={7} className="py-4 text-muted-foreground">
                  Loading…
                </td>
              </tr>
            )}
            {rows?.length === 0 && (
              <tr>
                <td colSpan={7} className="py-4 text-muted-foreground">
                  No cards yet. Print a batch above.
                </td>
              </tr>
            )}
            {rows?.map((row) => (
              <tr key={row.id} className="border-t">
                <td className="py-2 font-mono">…{row.code_last4}</td>
                <td>{STATE_WORDS[row.state]}</td>
                <td className="font-mono">{row.batch_id ? row.batch_id.slice(0, 8) : "—"}</td>
                <td>{row.created_at}</td>
                <td>{row.redemption_valid_until ?? "never expires"}</td>
                <td className="text-muted-foreground">{row.notes ?? ""}</td>
                <td className="space-x-2 text-right">
                  <button
                    type="button"
                    disabled={!canRevealCodes}
                    className="rounded border px-2 py-1 disabled:opacity-40"
                    onClick={() => setDialog({ kind: "reveal", row })}
                  >
                    Show code
                  </button>
                  <button
                    type="button"
                    disabled={!canIssue || row.state !== "UNUSED"}
                    className="rounded-md border border-destructive/40 px-2 py-1 text-destructive disabled:opacity-50"
                    onClick={() => setDialog({ kind: "revoke", row })}
                  >
                    Cancel card
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      {/* ---- the step-up dialog ---- */}
      {dialog && (
        <div role="dialog" aria-modal="true" className="rounded border p-4 space-y-3">
          <h2 className="text-lg font-semibold">
            {dialog.kind === "reveal"
              ? "Show this code"
              : dialog.kind === "revoke"
                ? "Cancel this card"
                : "Export a batch of codes"}
          </h2>
          <p className="text-sm text-muted-foreground">
            {dialog.kind === "reveal" ? (
              <>
                The code will be shown once here, and this reveal will be recorded permanently with your name
                and your reason. It is not a one-time view — a voucher code can be read again — so the record
                is what makes it accountable.
              </>
            ) : dialog.kind === "revoke" ? (
              <>
                This card stops working. It cannot be un-cancelled. A card that has already been redeemed
                cannot be cancelled here at all: that guest has access, and ending it is done from their
                session.
              </>
            ) : (
              <>
                Every code in batch {dialog.batch.slice(0, 8)} will be recovered and offered as a CSV. One
                record is written naming you, your reason and how many codes you took.
              </>
            )}
          </p>
          <label className="block text-sm">
            Reason (recorded, 4–500 characters)
            <input
              className="mt-1 w-full rounded border px-2 py-1"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={
                dialog.kind === "revoke" ? "batch lost in the post" : "guest at the desk, card unreadable"
              }
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
          <div className="flex gap-2">
            <button
              type="button"
              disabled={!canSubmit}
              className={
                dialog.kind === "revoke"
                  ? "rounded-md bg-destructive px-3 py-1 text-sm font-medium text-destructive-foreground disabled:opacity-50"
                  : "rounded-md bg-primary px-3 py-1 text-sm font-medium text-primary-foreground disabled:opacity-50"
              }
              onClick={submit}
            >
              {dialog.kind === "reveal" ? "Show it" : dialog.kind === "revoke" ? "Cancel the card" : "Export"}
            </button>
            <button type="button" className="rounded border px-3 py-1" onClick={closeDialog}>
              Cancel
            </button>
          </div>
        </div>
      )}

      {/* ---- who has looked ---- */}
      {canRevealCodes && (
        <section className="space-y-2">
          <h2 className="font-semibold">Who has read a code</h2>
          <p className="text-sm text-muted-foreground">
            Every reveal and every export, permanently. Nothing on this list can be edited or removed.
          </p>
          <table className="w-full text-sm">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="py-2">When</th>
                <th>What</th>
                <th>How many</th>
                <th>Who</th>
                <th>Why</th>
              </tr>
            </thead>
            <tbody>
              {reveals === null && (
                <tr>
                  <td colSpan={5} className="py-4 text-muted-foreground">
                    Loading…
                  </td>
                </tr>
              )}
              {reveals?.length === 0 && (
                <tr>
                  <td colSpan={5} className="py-4 text-muted-foreground">
                    Nobody has read a code yet.
                  </td>
                </tr>
              )}
              {reveals?.map((r, i) => (
                <tr key={`${r.revealed_at}-${i}`} className="border-t">
                  <td className="py-2">{r.revealed_at}</td>
                  <td>{r.action === "REVEAL" ? "One card" : "A batch export"}</td>
                  <td>{r.voucher_count}</td>
                  <td>{r.operator_label}</td>
                  <td className="text-muted-foreground">{r.reason}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}
    </div>
  );
}
