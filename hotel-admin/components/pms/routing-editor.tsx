"use client";

import { useEffect, useMemo, useState } from "react";
import { api, ApiError, type PmsInterface } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { METHOD_LABEL, type MethodKey } from "@/lib/sign-in-effective";
import { DialogForm } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Callout } from "@/components/ui/error-banner";
import { Checkbox } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { useToast } from "@/components/ui/toast";
import { POLICY_WORDS, type RoutingMode, type RoutingNote, type RoutingPlan, type RoutingState } from "@/lib/api/pms-routing";

export type ChoosableInterface = { id: string; label: string; lifecycle: string; published: boolean };

const MAX_SHARED = 3;
const STALE = new Set(["routing_changed", "preview_required", "proposal_mismatch"]);
const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const UNNAMED = "Unnamed PMS connection";

function routingError(e: unknown): Error {
  const code = e instanceof ApiError ? e.code : "";
  const status = e instanceof ApiError ? e.status : 0;
  if (code === "proposal_other_operator")
    return new Error("That preview was made by someone else. Choose Back and preview the change yourself. Nothing was changed.");
  if (code === "forbidden" || status === 403) return new Error("Your role cannot change room sign-in. Nothing was changed.");
  if (code === "not_found" || status === 404)
    return new Error("This network or one of the chosen PMS connections no longer exists here. Close and refresh the page. Nothing was changed.");
  if (code === "validation")
    return new Error("The chosen PMS connections do not fit this option. Choose again and preview the change. Nothing was changed.");
  if (code === "bad_request") return new Error("Something required was missing. Choose again and preview the change. Nothing was changed.");
  return new Error("The change could not be made. Nothing was changed; try again.");
}

function noteText(n: RoutingNote, labelOf: (id: string) => string): string {
  const who = n.pms_interface_id ? labelOf(n.pms_interface_id) : "";
  switch (n.code) {
    case "UNDECIDED_TAKES_NO_INTERFACES": return "Returning a network to not decided takes no PMS connection.";
    case "ALREADY_UNDECIDED": return "This network is already not decided.";
    case "NONE_TAKES_NO_INTERFACES": return "No PMS connection can be chosen when room sign-in is switched off for this network.";
    case "DEDICATED_NEEDS_ONE": return "One dedicated PMS needs exactly one PMS connection.";
    case "SHARED_NEEDS_TWO": return "A shared strict set needs at least two PMS connections. For one, choose One dedicated PMS.";
    case "SHARED_TOO_MANY": return `A network can check at most ${MAX_SHARED} PMS connections. Each one is asked on every sign-in, so the set is kept small on purpose.`;
    case "MODE_UNKNOWN": return "Choose No room sign-in, One dedicated PMS or A shared strict set.";
    case "NETWORK_ARCHIVED": return "This network is archived. Restore it from the archive before changing its room sign-in.";
    case "INTERFACE_NOT_FOUND": return "A chosen PMS connection does not exist at this site.";
    case "INTERFACE_NOT_PUBLISHED":
      if (who) return `${who} has no published configuration. Publish one under PMS connection before routing a client network to it.`;
      break;
  }
  return n.text.replace(/PMS interfaces/g, "PMS connections").replace(/PMS interface/g, "PMS connection").replace(/ — /g, ". ");
}

function methodWords(keys: string[]): string {
  return keys.map((k) => METHOD_LABEL[k as MethodKey] ?? "a method this console does not recognise").join(", ");
}

export function usePmsRouting(): { canRoute: boolean; interfaces: ChoosableInterface[] } {
  const roles = useOperatorRoles();
  const [interfaces, setInterfaces] = useState<ChoosableInterface[]>([]);
  const canRoute = roles !== null && canWrite("pms-routing", roles);
  useEffect(() => {
    if (!canRoute) return;
    api.get<{ interfaces: PmsInterface[] }>("/pms-interfaces").then((r) => setInterfaces((r.interfaces ?? [])
      .map((i) => ({ id: i.id, label: i.display_label || UNNAMED, lifecycle: i.lifecycle_state, published: i.published })))).catch(() => setInterfaces([]));
  }, [canRoute]);
  return { canRoute, interfaces };
}

const MODES: { value: RoutingMode; title: string; body: string; fits: string }[] = [
  { value: "NONE", title: "No room sign-in", fits: "Vouchers, staff or conference networks",
    body: "Guests use vouchers, accounts or the other sign-in methods. No PMS is asked on this network, and it is never reported as a fault." },
  { value: "DEDICATED", title: "One dedicated PMS", fits: "Most networks",
    body: "The room is checked against one PMS connection only. Several networks may use the same PMS." },
  { value: "SHARED_STRICT", title: "A shared strict set", fits: "One network served by two or three PMS",
    body: "Every selected PMS is asked. A guest is let in only when exactly one finds them and the others clearly do not. If one cannot answer, nobody is let in on this network, so no guess is ever made." },
];

function unavailable(i: ChoosableInterface): string | null {
  if (i.lifecycle !== "ACTIVE") return "not in service";
  if (!i.published) return "no published configuration";
  return null;
}

function sentence(mode: RoutingMode | "", labels: string[], network: string): string {
  if (mode === "NONE") return `Guests on ${network} will not see room sign-in. They use the other sign-in methods.`;
  if (mode === "DEDICATED" && labels.length === 1) return `Guests on ${network} will sign in with their room number, checked against ${labels[0]} only.`;
  if (mode === "SHARED_STRICT" && labels.length >= 2) {
    return `Guests on ${network} will sign in with their room number, checked against ${labels.join(" and ")} together. They are never asked which PMS they belong to.`;
  }
  return "";
}

export function RoutingEditor({
  open, network, interfaces, onClose, onDone, intro,
}: {
  open: boolean;
  network: { id: string; name: string } | null;
  interfaces: ChoosableInterface[];
  onClose: () => void;
  onDone: () => void | Promise<void>;
  intro?: React.ReactNode;
}) {
  const toast = useToast();
  const [current, setCurrent] = useState<RoutingState | null>(null);
  const [mode, setMode] = useState<RoutingMode | "">("");
  const [picked, setPicked] = useState<string[]>([]);
  const [plan, setPlan] = useState<RoutingPlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [reload, setReload] = useState(0);
  const [currentState, setCurrentState] = useState<"loading" | "loaded" | "failed">("loading");

  useEffect(() => {
    if (!open || !network) return;
    setPlan(null); setCurrent(null); setCurrentState("loading");
    let live = true;
    api.get<{ policies: RoutingState[] }>("/pms-routing").then((r) => {
      if (!live) return;
      const me = (r.policies ?? []).find((p) => p.network_id === network.id) ?? null;
      setCurrent(me);
      setMode((me?.mode as RoutingMode) || "");
      setPicked((me?.interfaces ?? []).map((i) => i.pms_interface_id));
      setCurrentState("loaded");
    }).catch(() => live && setCurrentState("failed"));
    return () => { live = false; };
  }, [open, network, reload]);

  useEffect(() => { if (open) setErr(null); }, [open, network]);

  const usable = useMemo(() => interfaces.filter((i) => i.lifecycle !== "DECOMMISSIONED"), [interfaces]);
  const inService = usable.filter((i) => !unavailable(i));
  const label = (id: string) => usable.find((i) => i.id === id)?.label || UNNAMED;

  const chosen = mode === "NONE" ? [] : picked;
  const ready = currentState === "loaded" && (
    mode === "NONE" ||
    (mode === "DEDICATED" && picked.length === 1) ||
    (mode === "SHARED_STRICT" && picked.length >= 2 && picked.length <= MAX_SHARED));

  const modeBlocked = (m: RoutingMode): string | null => {
    if (m === "DEDICATED" && inService.length < 1) return "No PMS connection is in service yet. Add or activate one under PMS connection.";
    if (m === "SHARED_STRICT" && inService.length < 2) return `Needs two PMS connections in service. ${inService.length === 0 ? "None is" : "Only one is"}.`;
    return null;
  };
  const recommended: RoutingMode = inService.length >= 1 ? "DEDICATED" : "NONE";

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
      setErr(routingError(e));
    } finally {
      setBusy(false);
    }
  }

  async function confirm() {
    if (!network || !plan || !mode) return;
    setBusy(true); setErr(null);
    try {
      await api.put(`/pms-routing/${network.id}`, { mode, pms_interface_ids: chosen, proposal_id: plan.proposal_id });
      await onDone();
      toast.success(`Room sign-in on ${network.name} is saved`, sentence(mode, chosen.map(label), network.name));
      onClose();
    } catch (e) {
      if (e instanceof ApiError && STALE.has(e.code)) {
        setPlan(null);
        setReload((n) => n + 1);
        setErr(new Error("That preview can no longer be confirmed: the network's room sign-in changed, or the preview was used or expired. It is reloaded here; choose again and preview the new state."));
      } else {
        setErr(routingError(e));
      }
    } finally {
      setBusy(false);
    }
  }

  const reviewing = plan !== null;
  const blocked = reviewing && plan!.blockers.length > 0;
  const summary = sentence(mode, chosen.map(label), network?.name ?? "this network");
  const stepText = reviewing
    ? "Step 2 of 2 · Review and confirm. Nothing changes until you confirm."
    : "Step 1 of 2 · Choose. You see the effect before anything changes.";

  return (
    <DialogForm
      open={open}
      onOpenChange={(v) => !v && onClose()}
      size="lg"
      title={`Room sign-in on ${network?.name ?? "this network"}`}
      description={stepText}
      submitLabel={reviewing ? "Confirm and apply" : "Preview the change"}
      busy={busy}
      error={err}
      disabled={reviewing ? blocked || !plan?.proposal_id : !ready}
      disabledReason={reviewing
        ? blocked ? "Resolve the problems above first." : "This preview cannot be confirmed. Choose Back and preview the change again."
        : currentState === "loading" ? "Loading the current choice…"
          : currentState === "failed" ? "The current choice could not be loaded."
            : !mode ? "Choose how room sign-in works." : "Choose the PMS connection(s) this option needs."}
      extraFooter={reviewing ? <Button type="button" variant="ghost" onClick={() => setPlan(null)} disabled={busy}>Back</Button> : undefined}
      onSubmit={() => (reviewing ? confirm() : review())}
    >
      {!reviewing && (
        <div className={`space-y-4 ${FADE}`}>
          {intro}
          {currentState === "failed" && (
            <Callout tone="warning" title="The current choice could not be loaded">
              <p>Nothing can be previewed until it loads. Nothing has been changed.</p>
              <Button type="button" size="sm" variant="secondary" className="mt-2" onClick={() => setReload((n) => n + 1)}>Try again</Button>
            </Callout>
          )}
          <div role="radiogroup" aria-label="Room sign-in on this network" className="space-y-2">
            {MODES.map((m) => {
              const why = modeBlocked(m.value);
              const id = `routing-mode-${m.value}`;
              return (
                <div key={m.value} className={`flex items-start gap-3 rounded-md border p-3 ${why ? "opacity-60" : ""} ${mode === m.value ? "border-primary bg-primary/5" : "border-border"}`}>
                  <input id={id} type="radio" name="routing-mode" className="mt-1" checked={mode === m.value} disabled={!!why} onChange={() => pickMode(m.value)} />
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <label htmlFor={id} className="block cursor-pointer text-sm font-medium">{m.title}</label>
                      {!why && m.value === recommended && <Badge tone="accent" size="sm">Recommended</Badge>}
                      <span className="text-xs text-muted-foreground">{m.fits}</span>
                    </div>
                    <p className="text-xs text-muted-foreground">{why ?? m.body}</p>
                  </div>
                </div>
              );
            })}
          </div>

          {(mode === "DEDICATED" || mode === "SHARED_STRICT") && (
            <fieldset className={`space-y-2 ${FADE}`}>
              <legend className="mb-1 text-label">
                {mode === "DEDICATED" ? "Which PMS connection?" : `Which PMS connections? Choose 2 to ${MAX_SHARED}`}
              </legend>
              {usable.map((i) => {
                const why = unavailable(i);
                const on = picked.includes(i.id);
                const atLimit = mode === "SHARED_STRICT" && !on && picked.length >= MAX_SHARED;
                return mode === "DEDICATED" ? (
                  <label key={i.id} className={`flex items-start gap-3 rounded-md border p-2.5 text-sm ${why ? "opacity-60" : "cursor-pointer"} ${on ? "border-primary" : "border-border"}`}>
                    <input type="radio" name="routing-iface" className="mt-1" checked={on} disabled={!!why && !on} onChange={() => setPicked([i.id])} />
                    <span>{i.label || UNNAMED}{why && <span className="ms-2 text-xs text-muted-foreground">({why})</span>}</span>
                  </label>
                ) : (
                  <Checkbox key={i.id} label={<>{i.label || UNNAMED}{why && <span className="ms-2 text-xs text-muted-foreground">({why})</span>}</>}
                    checked={on} disabled={(!!why || atLimit) && !on}
                    onChange={(e) => setPicked((p) => (e.target.checked ? [...p, i.id] : p.filter((x) => x !== i.id)))} />
                );
              })}
              {mode === "SHARED_STRICT" && (
                <p className="text-xs text-muted-foreground">
                  {picked.length < 2
                    ? `Choose at least ${2 - picked.length} more.`
                    : picked.length >= MAX_SHARED
                      ? `${MAX_SHARED} is the most a network can check: each one is asked on every sign-in.`
                      : "You can add one more, or continue."}
                </p>
              )}
            </fieldset>
          )}

          {summary && <p className="rounded-md bg-surface px-3 py-2 text-sm" data-testid="routing-summary">{summary}</p>}

          {current && current.mode !== "" && (
            <p className="text-xs text-muted-foreground">
              Now: {POLICY_WORDS[current.mode] ?? "Not recognised"}{current.interfaces.length > 0 ? ` (${current.interfaces.map((i) => i.label || UNNAMED).join(", ")})` : ""}.
            </p>
          )}
        </div>
      )}

      {reviewing && plan && (
        <div className={`space-y-3 text-sm ${FADE}`} data-testid="routing-preview">
          {summary && <p className="rounded-md bg-surface px-3 py-2 font-medium">{summary}</p>}
          <p className="text-muted-foreground">
            <strong>{POLICY_WORDS[plan.network.mode] ?? "Not decided"}</strong> → <strong>{POLICY_WORDS[plan.proposed.mode] ?? "Not recognised"}</strong>
            {plan.proposed.interfaces.length > 0 && <>: {plan.proposed.interfaces.map((i) => i.label || UNNAMED).join(", ")}</>}
          </p>

          {!plan.impact.network_serves_clients && (
            <Callout tone="info" title="This network is off">
              The choice is saved, but no guest can use it until the network is switched on under Client networks.
            </Callout>
          )}
          {plan.impact.sign_in_methods_switched_off.includes("pms") && plan.proposed.mode !== "NONE" ? (
            <Callout tone="warning" title="Room sign-in is switched off on this network">
              Room sign-in is switched off for this network under Client networks, so guests will not see it until it is switched back on there.
              {plan.impact.sign_in_methods_switched_off.length > 1 && <> Also switched off here: {methodWords(plan.impact.sign_in_methods_switched_off.filter((m) => m !== "pms"))}.</>}
            </Callout>
          ) : plan.impact.sign_in_methods_switched_off.length > 0 ? (
            <p className="text-muted-foreground">Sign-in methods switched off on this network: {methodWords(plan.impact.sign_in_methods_switched_off)}.</p>
          ) : null}

          {plan.blockers.length > 0 && (
            <Callout tone="danger" title="This cannot be applied">
              <ul className="list-disc ps-4">{plan.blockers.map((b) => <li key={b.code + (b.pms_interface_id ?? "")}>{noteText(b, label)}</li>)}</ul>
              <p className="mt-1">Choose Back to change the selection.</p>
            </Callout>
          )}
          {plan.warnings.length > 0 && (
            <Callout tone="warning" title="Check these first">
              <ul className="list-disc space-y-1 ps-4">{plan.warnings.map((w) => <li key={w.code + (w.pms_interface_id ?? "")}>{noteText(w, label)}</li>)}</ul>
            </Callout>
          )}

          <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-3">
            <div><dt className="text-muted-foreground">Clients online here</dt><dd className="font-medium tabular">{plan.impact.active_sessions.toLocaleString()}</dd></div>
            <div><dt className="text-muted-foreground">Of those with room access</dt><dd className="font-medium tabular">{plan.impact.live_room_entitlements.toLocaleString()}</dd></div>
            {plan.proposed.mode === "SHARED_STRICT" && (
              <div><dt className="text-muted-foreground">Rooms in more than one selected PMS</dt><dd className="font-medium tabular">{plan.impact.room_numbers_in_more_than_one_selected_pms.toLocaleString()}</dd></div>
            )}
          </dl>

          {plan.proposed.interfaces.length > 0 && (
            <ul className="divide-y divide-border rounded-md border border-border">
              {plan.proposed.interfaces.map((i) => (
                <li key={i.pms_interface_id} className="flex flex-wrap items-center gap-2 px-3 py-2 text-xs">
                  <span className="font-medium">{i.label || UNNAMED}</span>
                  <Badge tone={!i.health_known ? "neutral" : i.room_auth_ready ? "ok" : "warn"} size="sm">
                    {!i.health_known ? "State not known" : i.room_auth_ready ? "Can answer" : "Cannot answer now"}
                  </Badge>
                  <span className="text-muted-foreground">{i.in_house_stays.toLocaleString()} {i.in_house_stays === 1 ? "guest" : "guests"} in house</span>
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

export function ClearRoutingDialog({
  open, network, onClose, onDone,
}: {
  open: boolean;
  network: { id: string; name: string } | null;
  onClose: () => void;
  onDone: () => void | Promise<void>;
}) {
  const toast = useToast();
  const [plan, setPlan] = useState<RoutingPlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [again, setAgain] = useState(0);

  useEffect(() => {
    if (!open || !network) return;
    setPlan(null);
    let live = true;
    api.post<RoutingPlan>(`/pms-routing/${network.id}/preview`, { mode: "UNDECIDED", pms_interface_ids: [] })
      .then((p) => live && setPlan(p)).catch((e) => live && setErr(routingError(e)));
    return () => { live = false; };
  }, [open, network, again]);
  useEffect(() => { if (open) setErr(null); }, [open, network]);

  async function confirm() {
    if (!network || !plan?.proposal_id) return;
    setBusy(true); setErr(null);
    try {
      await api.del(`/pms-routing/${network.id}?proposal=${encodeURIComponent(plan.proposal_id)}`);
      await onDone();
      toast.success(`${network.name} is back to not decided`, "Room sign-in is not offered there until someone chooses how it works.");
      onClose();
    } catch (e) {
      if (e instanceof ApiError && STALE.has(e.code)) {
        setAgain((n) => n + 1);
        setErr(new Error("That preview can no longer be confirmed: the network's room sign-in changed, or the preview was used or expired. It is reloaded here."));
      } else {
        setErr(routingError(e));
      }
    } finally {
      setBusy(false);
    }
  }

  const blocked = !plan || plan.blockers.length > 0 || !plan.proposal_id;
  const clearReason = !plan
    ? err ? "The preview could not be made. Close and try again." : "Working out what would change…"
    : plan.blockers.some((b) => b.code === "ALREADY_UNDECIDED") ? "This network is already not decided."
      : plan.blockers.length > 0 ? "Resolve the problems above first."
        : "This preview cannot be confirmed. Close and try again.";
  const labelOf = (id: string) => plan?.network.interfaces.find((i) => i.pms_interface_id === id)?.label || UNNAMED;
  return (
    <DialogForm
      open={open}
      onOpenChange={(v) => !v && onClose()}
      title={`Return ${network?.name ?? "this network"} to not decided?`}
      description="Review what is removed. Nothing changes until you confirm."
      submitLabel="Return to not decided"
      submitVariant="danger"
      busy={busy}
      error={err}
      disabled={blocked}
      disabledReason={clearReason}
      onSubmit={confirm}
    >
      {!plan && !err && (
        <p role="status" className="text-sm text-muted-foreground">Working out what would change…</p>
      )}
      {plan && (
        <div className={`space-y-3 text-sm ${FADE}`} data-testid="routing-clear-preview">
          <p>
            <strong>{POLICY_WORDS[plan.network.mode] ?? "Not decided"}</strong>
            {plan.network.interfaces.length > 0 && <> ({plan.network.interfaces.map((i) => i.label || UNNAMED).join(", ")})</>} → <strong>Not decided</strong>
          </p>
          {plan.blockers.length > 0 && (
            <Callout tone="danger" title="This cannot be applied">
              <ul className="list-disc ps-4">{plan.blockers.map((b) => <li key={b.code}>{noteText(b, labelOf)}</li>)}</ul>
            </Callout>
          )}
          {plan.warnings.length > 0 && (
            <Callout tone="warning" title="Check these first">
              <ul className="list-disc ps-4">{plan.warnings.map((w) => <li key={w.code}>{noteText(w, labelOf)}</li>)}</ul>
            </Callout>
          )}
          <p className="text-muted-foreground">To say a network is deliberately PMS-free, choose “No room sign-in” instead: that is a decision, not a gap.</p>
          <ul className="list-disc space-y-1 ps-4 text-muted-foreground" aria-label="What this does not do">
            {plan.guarantees.map((g) => <li key={g}>{g}</li>)}
          </ul>
        </div>
      )}
    </DialogForm>
  );
}
