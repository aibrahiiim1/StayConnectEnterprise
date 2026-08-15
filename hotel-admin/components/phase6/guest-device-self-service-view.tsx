"use client";

// GUEST DEVICE SELF-SERVICE — the per-appliance product setting (Phase 6, DARK).
//
// THE HARD PART OF THIS SCREEN IS NOT THE SWITCH, IT IS THE TWO STATES BEHIND IT.
//
//   The PRODUCT SETTING is this property's decision: does this hotel offer guests the ability to remove
//   their own offline devices? It lives in the appliance's own database, it is what this screen changes, and
//   it survives restarts and works with no Central Control Plane.
//
//   The DEPLOYMENT GATE is whether the capability is deployed in this build at all. It is not a hotel
//   decision, this screen cannot change it, and while it is off no guest can reach the feature no matter
//   what the setting says.
//
// An operator who turns the setting on, sees it confirmed, and walks away believing guests can now manage
// their devices has been misled by the product -- so the two states are shown as two separate facts, and the
// screen says in plain words what the combination currently means. No text here may suggest that switching
// the setting on deploys anything.
//
// canAct decides whether the CONTROL is offered, from the same role matrix edged enforces. A read-only
// operator sees the state and no switch; if they forged the request anyway, edged would refuse it -- the
// hiding is courtesy, the refusal is the boundary.

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";

type Setting = {
  enabled: boolean;
  changed?: boolean;
  phase_gate_enabled: boolean;
};

export function GuestDeviceSelfServiceView({ canAct }: { canAct: boolean }) {
  const [state, setState] = useState<Setting | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<boolean | null>(null);
  const [reason, setReason] = useState("");
  const [saved, setSaved] = useState<string | null>(null);

  const load = useCallback(() => {
    api
      .get<Setting>("/guest-device-self-service/")
      .then((s) => {
        setState(s);
        setError(null);
      })
      .catch((e) => setError(String((e as { message?: string })?.message ?? e)));
  }, []);

  useEffect(load, [load]);

  async function save(next: boolean) {
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      const res = await api.put<Setting>("/guest-device-self-service/", {
        enabled: next,
        reason: reason.trim(),
      });
      setState(res);
      setSaved(
        res.changed
          ? next
            ? "Saved. This property now offers guest device self-service."
            : "Saved. This property no longer offers guest device self-service."
          : "No change — the setting was already in that state."
      );
      setPending(null);
      setReason("");
    } catch (e: unknown) {
      setError(String((e as { message?: string })?.message ?? e));
    } finally {
      setBusy(false);
    }
  }

  if (error && !state) {
    return (
      <div role="alert" className="rounded border border-red-300 bg-red-50 p-3 text-sm text-red-800">
        {error}
      </div>
    );
  }
  if (!state) return <div className="text-sm text-muted">Loading…</div>;

  const on = state.enabled;
  const deployed = state.phase_gate_enabled;

  return (
    <div className="space-y-6">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold">Guest device self-service</h1>
        <p className="text-sm text-muted">
          When this is on, a guest who is signed in can see the devices using their own allowance and remove
          one that is not currently connected, freeing its place. A device that is online is never removable,
          and a guest can only ever see their own devices.
        </p>
      </header>

      {error && (
        <div role="alert" className="rounded border border-red-300 bg-red-50 p-3 text-sm text-red-800">
          {error}
        </div>
      )}
      {saved && (
        <div role="status" className="rounded border border-green-300 bg-green-50 p-3 text-sm text-green-800">
          {saved}
        </div>
      )}

      {/* THE TWO STATES, SIDE BY SIDE AND NEVER MERGED INTO ONE INDICATOR. */}
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="rounded border border-border bg-panel p-4">
          <div className="text-[11px] uppercase tracking-widest text-muted">This property offers it</div>
          <div className="mt-1 text-lg font-semibold" data-testid="setting-state">
            {on ? "On" : "Off"}
          </div>
          <p className="mt-2 text-xs text-muted">
            Your setting, stored on this appliance. It applies as soon as it is saved and keeps working if
            the connection to StayConnect Central is unavailable.
          </p>
        </div>
        <div className="rounded border border-border bg-panel p-4">
          <div className="text-[11px] uppercase tracking-widest text-muted">Available in this release</div>
          <div className="mt-1 text-lg font-semibold" data-testid="gate-state">
            {deployed ? "Yes" : "Not yet"}
          </div>
          <p className="mt-2 text-xs text-muted">
            Whether the guest feature is included in the software running on this appliance. This is not a
            hotel setting and is not changed from this screen.
          </p>
        </div>
      </div>

      {/* WHAT THE COMBINATION ACTUALLY MEANS RIGHT NOW, in one sentence, so nobody has to work it out. */}
      <div
        role="note"
        data-testid="effect"
        className="rounded border border-border bg-panel2 p-3 text-sm"
      >
        {deployed
          ? on
            ? "Guests can use device self-service on this property now."
            : "Guests cannot use device self-service here, because this property has it switched off."
          : on
            ? "Guests cannot use device self-service yet: it is switched on for this property, and it is not included in the software running on this appliance. Saving this setting does not install it."
            : "Guests cannot use device self-service: it is not included in the software running on this appliance, and this property has it switched off."}
      </div>

      {!canAct && (
        <p className="text-sm text-muted" data-testid="readonly-note">
          Your role can see this setting but not change it. Ask a site administrator or the hotel IT manager.
        </p>
      )}

      {canAct && pending === null && (
        <div>
          <button
            type="button"
            onClick={() => setPending(!on)}
            className="rounded border border-border px-4 py-2 text-sm hover:bg-panel2"
          >
            {on ? "Switch off" : "Switch on"}
          </button>
        </div>
      )}

      {canAct && pending !== null && (
        <div className="space-y-3 rounded border border-border bg-panel p-4">
          <div className="text-sm font-medium">
            {pending
              ? "Offer guest device self-service at this property?"
              : "Stop offering guest device self-service at this property?"}
          </div>
          <p className="text-xs text-muted">
            {pending
              ? "Guests will be able to remove their own devices that are not connected. Devices that are online stay put."
              : "Guests will no longer see or be able to remove their devices. Nothing already connected is disconnected by this change."}
          </p>
          <label className="block text-sm">
            <span className="text-muted">Reason (recorded in the change history)</span>
            <input
              type="text"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Why are you making this change?"
              className="mt-1 w-full rounded border border-border bg-panel2 px-3 py-2 text-sm"
            />
          </label>
          <div className="flex gap-2">
            <button
              type="button"
              disabled={busy}
              onClick={() => save(pending)}
              className="rounded bg-blue-600 px-4 py-2 text-sm text-white disabled:opacity-50"
            >
              {busy ? "Saving…" : pending ? "Switch on" : "Switch off"}
            </button>
            <button
              type="button"
              disabled={busy}
              onClick={() => {
                setPending(null);
                setReason("");
              }}
              className="rounded border border-border px-4 py-2 text-sm"
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
