"use client";

// Choose how room sign-in works on ONE client network, see exactly what the change affects, then confirm that preview. Three explicit choices; "every active PMS" is
// not one of them. The change is applied against the preview's fingerprint, so only the state that was previewed can be replaced.

import { useEffect, useMemo, useState } from "react";
import { api, ApiError, type PmsInterface, type Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { DialogForm } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Callout } from "@/components/ui/error-banner";
import { Checkbox } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { POLICY_WORDS, type RoutingMode, type RoutingPlan, type RoutingState } from "@/lib/api/pms-routing";

export type ChoosableInterface = { id: string; label: string; lifecycle: string; published: boolean };

const MAX_SHARED = 3;

/** Routing is its own permission (pms-routing), separate from editing client networks: controls are hidden, never offered and refused. */
export function usePmsRouting(): { canRoute: boolean; interfaces: ChoosableInterface[] } {
  const [roles, setRoles] = useState<string[]>([]);
  const [interfaces, setInterfaces] = useState<ChoosableInterface[]>([]);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((me) => setRoles(me?.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const canRoute = canWrite("pms-routing", roles);
  useEffect(() => {
    if (!canRoute) return;
    api.get<{ interfaces: PmsInterface[] }>("/pms-interfaces").then((r) => setInterfaces((r.interfaces ?? [])
      .map((i) => ({ id: i.id, label: i.display_label || i.id, lifecycle: i.lifecycle_state, published: i.published })))).catch(() => setInterfaces([]));
  }, [canRoute]);
  return { canRoute, interfaces };
}

const MODES: { value: RoutingMode; title: string; body: string }[] = [
  { value: "NONE", title: "No room sign-in", body: "Guests use vouchers, accounts or the other methods. Nothing about any PMS is asked on this network, and it is never reported as a fault." },
  { value: "DEDICATED", title: "One dedicated PMS", body: "The room is checked against one PMS connection only. The normal choice for a site, a floor or a VLAN with its own PMS." },
  { value: "SHARED_STRICT", title: "A shared strict set", body: `Two or three independent PMS connections serve the same network. Every one is asked, and a guest is admitted only when exactly one finds them and the others clearly do not. If one cannot answer, nobody is admitted on this network, so none is guessed.` },
];

function unavailable(i: ChoosableInterface): string | null {
  if (i.lifecycle !== "ACTIVE") return "not in service";
  if (!i.published) return "no published configuration";
  return null;
}

export function RoutingEditor({
  open, network, interfaces, onClose, onDone, intro,
}: {
  open: boolean;
  network: { id: string; name: string } | null;
  interfaces: ChoosableInterface[];
  onClose: () => void;
  onDone: () => void | Promise<void>;
  /** Extra framing for the caller (for example "after the reset"). */
  intro?: React.ReactNode;
}) {
  const [current, setCurrent] = useState<RoutingState | null>(null);
  const [mode, setMode] = useState<RoutingMode | "">("");
  const [picked, setPicked] = useState<string[]>([]);
  const [plan, setPlan] = useState<RoutingPlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  // Start from what is configured, read fresh, so "Change" never starts on a stale pick.
  useEffect(() => {
    if (!open || !network) return;
    setPlan(null); setErr(null); setCurrent(null);
    let live = true;
    api.get<{ policies: RoutingState[] }>("/pms-routing").then((r) => {
      if (!live) return;
      const me = (r.policies ?? []).find((p) => p.network_id === network.id) ?? null;
      setCurrent(me);
      setMode((me?.mode as RoutingMode) || "");
      setPicked((me?.interfaces ?? []).map((i) => i.pms_interface_id));
    }).catch((e) => live && setErr(e));
    return () => { live = false; };
  }, [open, network]);

  const usable = useMemo(() => interfaces.filter((i) => i.lifecycle !== "DECOMMISSIONED"), [interfaces]);
  const label = (id: string) => usable.find((i) => i.id === id)?.label ?? id;

  const chosen = mode === "NONE" ? [] : picked;
  const ready =
    mode === "NONE" ||
    (mode === "DEDICATED" && picked.length === 1) ||
    (mode === "SHARED_STRICT" && picked.length >= 2 && picked.length <= MAX_SHARED);

  function pickMode(m: RoutingMode) {
    setMode(m);
    setPicked((p) => (m === "NONE" ? [] : m === "DEDICATED" ? p.slice(0, 1) : p));
  }

  async function review() {
    if (!network || !mode) return;
    setBusy(true); setErr(null);
    try {
      setPlan(await api.post<RoutingPlan>(`/pms-routing/${network.id}/preview`, { mode, pms_interface_ids: chosen }));
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  async function confirm() {
    if (!network || !plan || !mode) return;
    setBusy(true); setErr(null);
    try {
      await api.put(`/pms-routing/${network.id}`, { mode, pms_interface_ids: chosen, based_on: plan.network.fingerprint });
      await onDone();
      onClose();
    } catch (e) {
      if (e instanceof ApiError && e.code === "routing_changed") setPlan(null);
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  const reviewing = plan !== null;
  const blocked = reviewing && plan!.blockers.length > 0;

  return (
    <DialogForm
      open={open}
      onOpenChange={(v) => !v && onClose()}
      size="lg"
      title={`Room sign-in on ${network?.name ?? "this network"}`}
      description={reviewing ? "Check what this does before you confirm." : "Choose how a guest on this network signs in with their room number."}
      submitLabel={reviewing ? "Confirm and apply" : "Preview the change"}
      busy={busy}
      error={err}
      disabled={reviewing ? blocked : !ready}
      disabledReason={reviewing ? "Resolve the problems above first." : "Choose an option, and the PMS connections it needs."}
      extraFooter={reviewing ? <Button type="button" variant="ghost" onClick={() => setPlan(null)} disabled={busy}>Back</Button> : undefined}
      onSubmit={() => (reviewing ? confirm() : review())}
    >
      {!reviewing && (
        <>
          {intro}
          <div role="radiogroup" aria-label="Room sign-in on this network" className="space-y-2">
            {MODES.map((m) => (
              <label key={m.value} className={`flex cursor-pointer items-start gap-3 rounded-md border p-3 ${mode === m.value ? "border-primary bg-primary/5" : "border-border"}`}>
                <input type="radio" name="routing-mode" className="mt-1" checked={mode === m.value} onChange={() => pickMode(m.value)} />
                <span className="min-w-0">
                  <span className="block text-sm font-medium">{m.title}</span>
                  <span className="block text-xs text-muted-foreground">{m.body}</span>
                </span>
              </label>
            ))}
          </div>

          {(mode === "DEDICATED" || mode === "SHARED_STRICT") && (
            <fieldset className="space-y-2">
              <legend className="mb-1 text-label">
                {mode === "DEDICATED" ? "PMS connection" : `PMS connections (choose 2 to ${MAX_SHARED})`}
              </legend>
              {usable.length === 0 && <p className="text-sm text-muted-foreground">No PMS connection exists yet. Add one under PMS connection first.</p>}
              {usable.map((i) => {
                const why = unavailable(i);
                const on = picked.includes(i.id);
                const atLimit = mode === "SHARED_STRICT" && !on && picked.length >= MAX_SHARED;
                return mode === "DEDICATED" ? (
                  <label key={i.id} className={`flex items-start gap-3 rounded-md border p-2.5 text-sm ${why ? "opacity-60" : "cursor-pointer"} ${on ? "border-primary" : "border-border"}`}>
                    <input type="radio" name="routing-iface" className="mt-1" checked={on} disabled={!!why} onChange={() => setPicked([i.id])} />
                    <span>{i.label || i.id}{why && <span className="ms-2 text-xs text-muted-foreground">({why})</span>}</span>
                  </label>
                ) : (
                  <Checkbox key={i.id} label={<>{i.label || i.id}{why && <span className="ms-2 text-xs text-muted-foreground">({why})</span>}</>}
                    checked={on} disabled={!!why || atLimit}
                    onChange={(e) => setPicked((p) => (e.target.checked ? [...p, i.id] : p.filter((x) => x !== i.id)))} />
                );
              })}
              {mode === "SHARED_STRICT" && picked.length >= MAX_SHARED && (
                <p className="text-xs text-muted-foreground">At most {MAX_SHARED} connections can share a network: each one is asked on every sign-in.</p>
              )}
            </fieldset>
          )}

          {current && current.mode !== "" && (
            <p className="text-xs text-muted-foreground">
              Now: {POLICY_WORDS[current.mode]}{current.interfaces.length > 0 ? ` — ${current.interfaces.map((i) => i.label).join(", ")}` : ""}.
            </p>
          )}
        </>
      )}

      {reviewing && plan && (
        <div className="space-y-3 text-sm" data-testid="routing-preview">
          <p>
            <strong>{POLICY_WORDS[plan.network.mode] ?? "Not decided"}</strong> → <strong>{POLICY_WORDS[plan.proposed.mode] ?? plan.proposed.mode}</strong>
            {plan.proposed.interfaces.length > 0 && <> — {plan.proposed.interfaces.map((i) => i.label).join(", ")}</>}
          </p>

          {plan.blockers.length > 0 && (
            <Callout tone="danger" title="This cannot be applied">
              <ul className="list-disc ps-4">{plan.blockers.map((b) => <li key={b.code + (b.pms_interface_id ?? "")}>{b.text}</li>)}</ul>
            </Callout>
          )}
          {plan.warnings.length > 0 && (
            <Callout tone="warning" title="Check these">
              <ul className="list-disc space-y-1 ps-4">{plan.warnings.map((w) => <li key={w.code + (w.pms_interface_id ?? "")}>{w.text}</li>)}</ul>
            </Callout>
          )}

          <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-3">
            <div><dt className="text-muted-foreground">Clients online here</dt><dd className="font-medium tabular">{plan.impact.active_sessions}</dd></div>
            <div><dt className="text-muted-foreground">Of those with room access</dt><dd className="font-medium tabular">{plan.impact.live_room_entitlements}</dd></div>
            <div><dt className="text-muted-foreground">Rooms in more than one selected PMS</dt><dd className="font-medium tabular">{plan.impact.room_numbers_in_more_than_one_selected_pms}</dd></div>
          </dl>

          {plan.proposed.interfaces.length > 0 && (
            <ul className="divide-y divide-border rounded-md border border-border">
              {plan.proposed.interfaces.map((i) => (
                <li key={i.pms_interface_id} className="flex flex-wrap items-center gap-2 px-3 py-2 text-xs">
                  <span className="font-medium">{i.label}</span>
                  <Badge tone={!i.health_known ? "neutral" : i.room_auth_ready ? "ok" : "warn"} size="sm">
                    {!i.health_known ? "state not known" : i.room_auth_ready ? "can answer" : "cannot answer now"}
                  </Badge>
                  <span className="text-muted-foreground">{i.in_house_stays} guests in house</span>
                  {i.also_used_by.length > 0 && <span className="text-muted-foreground">also used by {i.also_used_by.join(", ")}</span>}
                </li>
              ))}
            </ul>
          )}

          <ul className="list-disc space-y-1 ps-4 text-muted-foreground" aria-label="What this does not do">
            {plan.guarantees.map((g) => <li key={g}>{g}</li>)}
          </ul>
        </div>
      )}
    </DialogForm>
  );
}
