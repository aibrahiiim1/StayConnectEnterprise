"use client";

import { ImpactPanel } from "@/components/network/impact-panel";
import { useEffect, useState } from "react";
import { Layers, Plus, Rocket, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import {
  CHANGE_VERB, FactoryInfo, GROUP_LABEL, PORT_MODE_LABEL, PortInfo, PortModePlan, PortNetwork, StagedGroup,
} from "@/lib/api/network-lifecycle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SectionCard } from "@/components/ui/card";
import { DialogForm } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Checkbox, Field, Input } from "@/components/ui/input";
import { useToast } from "@/components/ui/toast";
import { netErrorWords } from "@/components/network/shared";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

export type LoadReport = (ok: boolean) => void;

function absent(e: unknown): boolean {
  return e instanceof ApiError && e.status === 404;
}

export function FactoryNetworkCard({ writable, onStaged, onLoaded }: { writable: boolean; onStaged: () => void; onLoaded?: LoadReport }) {
  const [info, setInfo] = useState<FactoryInfo | null>(null);
  const [open, setOpen] = useState(false);
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const toast = useToast();

  useEffect(() => {
    api.get<FactoryInfo>("/network/factory")
      .then((r) => { setInfo(r); onLoaded?.(true); })
      .catch((e) => { setInfo(null); onLoaded?.(absent(e)); });
  }, [onLoaded]);

  if (!info || !["prepared", "blocked", "awaiting_assignment", "lan_unknown"].includes(info.state)) return null;
  const t = info.template;

  async function activate() {
    setBusy(true);
    setErr(null);
    try {
      await api.post("/network/factory/activate", { no_other_dhcp_server: true });
      setOpen(false);
      toast.success("Activation is staged", "Review and apply it from this screen. The network serves clients only after the change is confirmed.");
      onStaged();
      setInfo({ ...info!, state: "staged" });
    } catch (e) {
      setErr(netErrorWords(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <SectionCard
      title="Factory network"
      description={info.reason}
      actions={
        writable && info.state === "prepared" ? (
          <Button size="sm" onClick={() => { setAck(false); setErr(null); setOpen(true); }}>
            <Rocket /> Activate…
          </Button>
        ) : undefined
      }
    >
      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        <div><dt className="text-muted-foreground">Name</dt><dd>{t.name}</dd></div>
        <div><dt className="text-muted-foreground">Mode</dt><dd>{t.mode}</dd></div>
        <div><dt className="text-muted-foreground">Subnet</dt><dd className="font-mono">{t.subnet_cidr} (gateway {t.gateway_ip})</dd></div>
        <div><dt className="text-muted-foreground">DHCP</dt><dd>{t.dhcp}, <span className="font-mono">{t.pool.start_ip}–{t.pool.end_ip}</span></dd></div>
        <div className="sm:col-span-2"><dt className="text-muted-foreground">Prepared services</dt><dd>{t.services.join(", ")}</dd></div>
        <div className="sm:col-span-2"><dt className="text-muted-foreground">PMS</dt><dd>{t.pms}</dd></div>
      </dl>
      {info.conflicts.length > 0 && (
        <Callout tone="warning" title="Cannot be activated as it stands">
          <ul className="list-disc space-y-1 ps-5">{info.conflicts.map((c) => <li key={c}>{c}</li>)}</ul>
        </Callout>
      )}
      <DialogForm
        open={open}
        onOpenChange={setOpen}
        title="Activate the factory network"
        description="This stages the network. Nothing changes for clients until you apply it and then confirm it."
        submitLabel="Stage activation"
        busy={busy}
        error={err}
        disabled={!ack}
        disabledReason="Confirm that no other DHCP server serves this segment."
        onSubmit={activate}
      >
        <p className="text-sm text-muted-foreground">
          {t.name}: {t.subnet_cidr}, gateway {t.gateway_ip}, DHCP {t.pool.start_ip}–{t.pool.end_ip}, on {info.lan_interface ?? "the client port"}.
        </p>
        <ul className="list-disc space-y-1 ps-5 text-sm">{info.checklist.map((c) => <li key={c}>{c}</li>)}</ul>
        <Checkbox label="No other DHCP server serves this LAN segment." checked={ack} onChange={(e) => setAck(e.target.checked)} />
      </DialogForm>
    </SectionCard>
  );
}

export function StagedChangesList({ writable, reloadKey, onCancelled, onLoaded }: { writable: boolean; reloadKey: number; onCancelled: () => void; onLoaded?: LoadReport }) {
  const [groups, setGroups] = useState<StagedGroup[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [cancelling, setCancelling] = useState<string | null>(null);
  const toast = useToast();

  useEffect(() => {
    api.get<{ groups: StagedGroup[] }>("/network/changes")
      .then((r) => { setGroups(r.groups ?? []); onLoaded?.(true); })
      .catch((e) => { setGroups([]); onLoaded?.(absent(e)); });
  }, [reloadKey, onLoaded]);

  if (groups.length === 0) return null;

  async function cancel(g: StagedGroup) {
    if (cancelling) return;
    setErr(null);
    setCancelling(g.group_id);
    try {
      await api.del(`/network/changes/${g.group_id}`);
      toast.success(`${GROUP_LABEL[g.group_kind]} cancelled`, "Nothing had been applied.");
      onCancelled();
    } catch (e) {
      setErr(netErrorWords(e));
    } finally {
      setCancelling(null);
    }
  }

  return (
    <SectionCard className={FADE} title="Staged changes" description="Nothing here is live until you apply and keep the change.">
      <ErrorBanner err={err} className="mb-3" />
      <ul className="space-y-2">
        {groups.map((g) => (
          <li key={g.group_id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
            <span className="min-w-0 flex-1">
              <strong>{GROUP_LABEL[g.group_kind]}</strong>:{" "}
              {g.changes.map((c) => `${CHANGE_VERB[c.kind]} ${c.network || "an unnamed client network"}`).join("; ")}
            </span>
            <span className="flex items-center gap-2">
              {g.state === "PENDING" && <Badge tone="warn">Not applied yet</Badge>}
              {g.state === "APPLIED" && <Badge tone="info">Awaiting confirmation</Badge>}
              {writable && g.state === "PENDING" && (
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={cancelling !== null}
                  onClick={() => void cancel(g)}
                  aria-label={`Cancel ${GROUP_LABEL[g.group_kind]}`}
                >
                  <X /> {cancelling === g.group_id ? "Cancelling…" : "Cancel"}
                </Button>
              )}
            </span>
          </li>
        ))}
      </ul>
    </SectionCard>
  );
}

type VlanRow = { vlan: string; name: string };

export function portSummary(p: PortInfo): string {
  const tagged = p.vlans.length;
  if (p.mode === "trunk" && p.native) {
    return `Trunk: carries the untagged (native) network ${p.native.name} and ${tagged} tagged VLAN${tagged === 1 ? "" : "s"} side by side. They stay separate networks.`;
  }
  if (p.mode === "trunk") return `Trunk: carries ${tagged} tagged VLAN${tagged === 1 ? "" : "s"} and no untagged network.`;
  if (p.mode === "access") return `Access: carries one untagged network, ${p.native?.name ?? "with no name"}. Adding VLANs makes it a trunk and keeps this network as the native one.`;
  return "No network on this port is serving clients.";
}

function PortNetworkLine({ n, how, state }: { n: PortNetwork; how: string; state: "serving" | "off" }) {
  return (
    <li className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
      <Badge tone="neutral" size="sm">{how}</Badge>
      <span className={state === "off" ? "text-muted-foreground" : "font-medium"}>{n.name}</span>
      {state === "off" && <Badge tone="neutral" size="sm">Switched off</Badge>}
      <span className="font-mono text-xs text-muted-foreground">{n.subnet_cidr}</span>
      {state === "serving" && <span className="text-xs text-muted-foreground">{n.active_clients.toLocaleString()} connected</span>}
    </li>
  );
}

function vlanBad(v: string): boolean {
  const t = v.trim();
  if (t === "") return false;
  if (!/^\d+$/.test(t)) return true;
  const n = Number(t);
  return n < 1 || n > 4094;
}

export function PortModeCard({ writable, reloadKey, onStaged, onLoaded }: { writable: boolean; reloadKey: number; onStaged: () => void; onLoaded?: LoadReport }) {
  const [ports, setPorts] = useState<PortInfo[]>([]);
  const [target, setTarget] = useState<{ port: PortInfo; mode: "trunk" | "access" } | null>(null);
  const [rows, setRows] = useState<VlanRow[]>([{ vlan: "", name: "" }]);
  const [plan, setPlan] = useState<PortModePlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const toast = useToast();

  useEffect(() => {
    api.get<{ ports: PortInfo[] }>("/network/ports")
      .then((r) => { setPorts((r.ports ?? []).filter((p) => !p.protected)); onLoaded?.(true); })
      .catch((e) => { setPorts([]); onLoaded?.(absent(e)); });
  }, [reloadKey, onLoaded]);

  if (ports.length === 0) return null;

  function body(preview: boolean) {
    if (!target) return {};
    if (target.mode === "access") return { mode: "access", preview };
    return {
      mode: "trunk",
      preview,
      vlans: rows.filter((r) => r.vlan.trim() !== "").map((r) => ({ vlan_id: Number(r.vlan), name: r.name.trim() })),
    };
  }

  async function show() {
    setBusy(true);
    setErr(null);
    try {
      setPlan(await api.post<PortModePlan>(`/network/ports/${target!.port.name}/mode`, body(true)));
    } catch (e) {
      setPlan(null);
      setErr(netErrorWords(e));
    } finally {
      setBusy(false);
    }
  }

  async function stage() {
    setBusy(true);
    setErr(null);
    try {
      await api.post(`/network/ports/${target!.port.name}/mode`, body(false));
      toast.success("Port conversion is staged", "Review and apply it. It is applied, confirmed or rolled back as one change.");
      setTarget(null);
      setPlan(null);
      onStaged();
    } catch (e) {
      setErr(netErrorWords(e));
    } finally {
      setBusy(false);
    }
  }

  const anyBad = rows.some((r) => vlanBad(r.vlan));
  const noVlan = rows.every((r) => r.vlan.trim() === "");

  function open(port: PortInfo, mode: "trunk" | "access") {
    setTarget({ port, mode });
    setRows([{ vlan: "", name: "" }]);
    setPlan(null);
    setErr(null);
  }

  return (
    <SectionCard title="Client ports">
      <ul className="space-y-4">
        {ports.map((p) => (
          <li key={p.name} className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0 flex-1">
              <span className="font-mono">{p.name}</span>{" "}
              <Badge tone={p.mode === "trunk" ? "info" : p.mode === "access" ? "default" : "neutral"}>{PORT_MODE_LABEL[p.mode]}</Badge>
              <p className="mt-1 text-caption text-muted-foreground">{portSummary(p)}</p>
              <ul className="mt-2 space-y-1.5 text-sm" aria-label={`Networks on ${p.name}`}>
                {p.native && <PortNetworkLine n={p.native} how="Untagged (native)" state="serving" />}
                {p.vlans.map((v) => <PortNetworkLine key={v.id} n={v} how={`Tagged VLAN ${v.vlan_id}`} state="serving" />)}
                {(p.switched_off ?? []).map((v) => <PortNetworkLine key={v.id} n={v} how={v.vlan_id ? `Tagged VLAN ${v.vlan_id}` : "Untagged (native)"} state="off" />)}
              </ul>
              {p.staged_changes > 0 && <p className="mt-1 text-caption text-muted-foreground">{p.staged_changes === 1 ? "1 change staged." : `${p.staged_changes} changes staged.`}</p>}
            </div>
            {writable && (
              <div className="flex gap-2">
                {p.mode !== "unused" && (
                  <Button variant="secondary" size="sm" onClick={() => open(p, "trunk")}><Layers /> {p.mode === "trunk" ? "Add VLANs…" : "Convert to Trunk…"}</Button>
                )}
                {p.mode === "trunk" && p.native && (
                  <Button variant="secondary" size="sm" onClick={() => open(p, "access")}>Convert to Access…</Button>
                )}
              </div>
            )}
          </li>
        ))}
      </ul>

      <DialogForm
        open={target !== null}
        onOpenChange={(v) => !v && setTarget(null)}
        title={target?.mode === "access" ? `Convert ${target.port.name} to Access` : `${target?.port.mode === "trunk" ? "Add VLANs to" : "Convert"} ${target?.port.name}${target?.port.mode === "trunk" ? "" : " to Trunk"}`}
        description="You see what would change before anything is staged. Nothing is live until you apply and confirm."
        size="lg"
        submitLabel={plan ? "Stage this change" : "Preview"}
        busy={busy}
        error={err}
        disabled={plan ? plan.conflicts.length > 0 : target?.mode === "trunk" && (noVlan || anyBad)}
        disabledReason={plan ? "Resolve the conflicts first." : anyBad ? "A VLAN ID is a whole number from 1 to 4094." : "Give at least one VLAN ID."}
        onSubmit={() => (plan ? stage() : show())}
      >
        {target?.mode === "trunk" && (
          <div className="space-y-2">
            {rows.map((r, i) => (
              <div key={i} className="grid grid-cols-[7rem_1fr_auto] items-end gap-2">
                <Field label={i === 0 ? "VLAN ID" : undefined} error={vlanBad(r.vlan) ? "1 to 4094" : undefined}>
                  <Input inputMode="numeric" value={r.vlan} aria-label={`VLAN ID ${i + 1}`} aria-invalid={vlanBad(r.vlan) || undefined}
                    onChange={(e) => { setPlan(null); setRows(rows.map((x, j) => (j === i ? { ...x, vlan: e.target.value } : x))); }} />
                </Field>
                <Field label={i === 0 ? "Name" : undefined} optional={i === 0}>
                  <Input value={r.name} aria-label={`Name ${i + 1}`}
                    onChange={(e) => { setPlan(null); setRows(rows.map((x, j) => (j === i ? { ...x, name: e.target.value } : x))); }} />
                </Field>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  disabled={rows.length === 1}
                  aria-label={`Remove VLAN row ${i + 1}`}
                  onClick={() => { setPlan(null); setRows(rows.filter((_, j) => j !== i)); }}
                >
                  <X />
                </Button>
              </div>
            ))}
            <Button type="button" variant="ghost" size="sm" onClick={() => setRows([...rows, { vlan: "", name: "" }])}><Plus /> Add another VLAN</Button>
          </div>
        )}
        {plan && (
          <div className={`space-y-3 text-sm ${FADE}`}>
            {plan.conflicts.length > 0 && (
              <Callout tone="warning" title="Conflicts">
                <ul className="list-disc space-y-1 ps-5">{plan.conflicts.map((c) => <li key={c}>{c}</li>)}</ul>
              </Callout>
            )}
            {plan.create.length > 0 && (
              <div>
                <strong>Networks that would be created (switched on when applied)</strong>
                <ul className="list-disc ps-5">
                  {plan.create.map((c) => <li key={c.vlan_id}>VLAN {c.vlan_id} “{c.name}”: <span className="font-mono">{c.subnet_cidr}</span>, gateway <span className="font-mono">{c.gateway_ip}</span></li>)}
                </ul>
              </div>
            )}
            {plan.switch_off.length > 0 && (
              <div>
                <strong>Networks that would be switched off</strong>
                <ul className="list-disc ps-5">{plan.switch_off.map((n) => <li key={n.id}>{n.name} (VLAN {n.vlan_id}), {n.active_clients.toLocaleString()} connected</li>)}</ul>
              </div>
            )}
            <div>
              <strong>What this does to clients</strong>
              <ul className="list-disc ps-5">{plan.impact.map((i) => <li key={i}>{i}</li>)}</ul>
            </div>
            <ImpactPanel affected={plan.affected} />
            <Callout tone="info" title="Check outside the appliance">
              <p className="mb-1">The appliance cannot verify these.</p>
              <ul className="list-disc ps-5">{[...plan.switch_requirements, ...(plan.external_requirements ?? [])].map((i) => <li key={i}>{i}</li>)}</ul>
            </Callout>
          </div>
        )}
      </DialogForm>
    </SectionCard>
  );
}
