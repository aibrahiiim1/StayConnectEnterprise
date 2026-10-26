"use client";

import { ImpactPanel } from "@/components/network/impact-panel";
import { useEffect, useState } from "react";
import { Layers, Rocket, X } from "lucide-react";
import { api } from "@/lib/api";
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
import { errMsg } from "@/lib/utils";

/** The prepared Access network: shown until it is activated, never as if it were already serving. */
export function FactoryNetworkCard({ writable, onStaged }: { writable: boolean; onStaged: () => void }) {
  const [info, setInfo] = useState<FactoryInfo | null>(null);
  const [open, setOpen] = useState(false);
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();

  useEffect(() => {
    api.get<FactoryInfo>("/network/factory").then(setInfo).catch(() => setInfo(null));
  }, []);

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
      setErr(e);
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
          <ul className="list-disc space-y-1 pl-5">{info.conflicts.map((c) => <li key={c}>{c}</li>)}</ul>
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
        <ul className="list-disc space-y-1 pl-5 text-sm">{info.checklist.map((c) => <li key={c}>{c}</li>)}</ul>
        <Checkbox label="No other DHCP server serves this LAN segment." checked={ack} onChange={(e) => setAck(e.target.checked)} />
      </DialogForm>
    </SectionCard>
  );
}

/** Operations staged together (a port conversion, an activation) and waiting to be applied; one can be cancelled until it is applied. */
export function StagedChangesList({ writable, reloadKey, onCancelled }: { writable: boolean; reloadKey: number; onCancelled: () => void }) {
  const [groups, setGroups] = useState<StagedGroup[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const toast = useToast();

  useEffect(() => {
    api.get<{ groups: StagedGroup[] }>("/network/changes").then((r) => setGroups(r.groups ?? [])).catch(() => setGroups([]));
  }, [reloadKey]);

  if (groups.length === 0) return null;

  async function cancel(g: StagedGroup) {
    setErr(null);
    try {
      await api.del(`/network/changes/${g.group_id}`);
      toast.success(`${GROUP_LABEL[g.group_kind]} cancelled`, "Nothing had been applied.");
      onCancelled();
    } catch (e) {
      setErr(errMsg(e));
    }
  }

  return (
    <SectionCard title="Staged changes" description="Waiting to be applied. Nothing here is live yet: the networks stay as they are until you press Apply changes and keep the change.">
      <ErrorBanner err={err} className="mb-3" />
      <ul className="space-y-2">
        {groups.map((g) => (
          <li key={g.group_id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
            <span>
              <strong>{GROUP_LABEL[g.group_kind]}</strong>:{" "}
              {g.changes.map((c) => `${CHANGE_VERB[c.kind]} ${c.network || c.network_id}`).join("; ")}
            </span>
            {writable && g.state === "PENDING" && (
              <Button variant="ghost" size="sm" onClick={() => void cancel(g)} aria-label={`Cancel ${GROUP_LABEL[g.group_kind]}`}>
                <X /> Cancel
              </Button>
            )}
            {g.state === "PENDING" && <Badge tone="warn">Not applied yet</Badge>}
            {g.state === "APPLIED" && <Badge tone="info">Awaiting confirmation</Badge>}
          </li>
        ))}
      </ul>
    </SectionCard>
  );
}

type VlanRow = { vlan: string; name: string };

/** One sentence on what the port carries; a native network and tagged VLANs on one trunk is an ordinary, supported arrangement. */
export function portSummary(p: PortInfo): string {
  const tagged = p.vlans.length;
  if (p.mode === "trunk" && p.native) {
    return `Trunk: carries the untagged (native) network ${p.native.name} and ${tagged} tagged VLAN${tagged === 1 ? "" : "s"} side by side. They stay separate networks.`;
  }
  if (p.mode === "trunk") return `Trunk: carries ${tagged} tagged VLAN${tagged === 1 ? "" : "s"} and no untagged network.`;
  if (p.mode === "access") return `Access: carries one untagged network, ${p.native?.name ?? "—"}. Adding VLANs makes it a trunk and keeps this network as the native one.`;
  return "No network on this port is serving clients.";
}

function PortNetworkLine({ n, how, state }: { n: PortNetwork; how: string; state: "serving" | "off" }) {
  return (
    <li className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
      <Badge tone="neutral" size="sm">{how}</Badge>
      <span className={state === "off" ? "text-muted-foreground" : "font-medium"}>{n.name}</span>
      {state === "off" && <Badge tone="neutral" size="sm">Switched off</Badge>}
      <span className="font-mono text-xs text-muted-foreground">{n.subnet_cidr}</span>
      {state === "serving" && <span className="text-xs text-muted-foreground">{n.active_clients} connected</span>}
    </li>
  );
}

/** Access / Trunk per port, with a previewed, staged, atomic conversion. */
export function PortModeCard({ writable, reloadKey, onStaged }: { writable: boolean; reloadKey: number; onStaged: () => void }) {
  const [ports, setPorts] = useState<PortInfo[]>([]);
  const [target, setTarget] = useState<{ port: PortInfo; mode: "trunk" | "access" } | null>(null);
  const [rows, setRows] = useState<VlanRow[]>([{ vlan: "", name: "" }]);
  const [plan, setPlan] = useState<PortModePlan | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();

  useEffect(() => {
    api.get<{ ports: PortInfo[] }>("/network/ports").then((r) => setPorts((r.ports ?? []).filter((p) => !p.protected))).catch(() => setPorts([]));
  }, [reloadKey]);

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
      setErr(e);
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
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  function open(port: PortInfo, mode: "trunk" | "access") {
    setTarget({ port, mode });
    setRows([{ vlan: "", name: "" }]);
    setPlan(null);
    setErr(null);
  }

  return (
    <SectionCard title="Client ports" description="What each physical port carries. A port is a wire; each network on it is its own client network with its own purpose, addresses and sign-in.">
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
              {p.staged_changes > 0 && <p className="mt-1 text-caption text-muted-foreground">{p.staged_changes} change(s) staged.</p>}
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
        disabled={plan ? plan.conflicts.length > 0 : target?.mode === "trunk" && rows.every((r) => r.vlan.trim() === "")}
        disabledReason={plan ? "Resolve the conflicts first." : "Give at least one VLAN id."}
        onSubmit={() => (plan ? stage() : show())}
      >
        {target?.mode === "trunk" && (
          <div className="space-y-2">
            {rows.map((r, i) => (
              <div key={i} className="grid grid-cols-[7rem_1fr] gap-2">
                <Field label={i === 0 ? "VLAN id" : ""}>
                  <Input inputMode="numeric" value={r.vlan} aria-label={`VLAN id ${i + 1}`}
                    onChange={(e) => { setPlan(null); setRows(rows.map((x, j) => (j === i ? { ...x, vlan: e.target.value } : x))); }} />
                </Field>
                <Field label={i === 0 ? "Name (optional)" : ""}>
                  <Input value={r.name} aria-label={`Name ${i + 1}`}
                    onChange={(e) => { setPlan(null); setRows(rows.map((x, j) => (j === i ? { ...x, name: e.target.value } : x))); }} />
                </Field>
              </div>
            ))}
            <Button type="button" variant="ghost" size="sm" onClick={() => setRows([...rows, { vlan: "", name: "" }])}>Add another VLAN</Button>
          </div>
        )}
        {plan && (
          <div className="space-y-3 text-sm">
            {plan.conflicts.length > 0 && (
              <Callout tone="warning" title="Conflicts">
                <ul className="list-disc space-y-1 pl-5">{plan.conflicts.map((c) => <li key={c}>{c}</li>)}</ul>
              </Callout>
            )}
            {plan.create.length > 0 && (
              <div>
                <strong>Networks that would be created (switched on when applied)</strong>
                <ul className="list-disc pl-5">
                  {plan.create.map((c) => <li key={c.vlan_id}>VLAN {c.vlan_id} “{c.name}” — <span className="font-mono">{c.subnet_cidr}</span>, gateway <span className="font-mono">{c.gateway_ip}</span></li>)}
                </ul>
              </div>
            )}
            {plan.switch_off.length > 0 && (
              <div>
                <strong>Networks that would be switched off</strong>
                <ul className="list-disc pl-5">{plan.switch_off.map((n) => <li key={n.id}>{n.name} (VLAN {n.vlan_id}) — {n.active_clients} connected</li>)}</ul>
              </div>
            )}
            <div>
              <strong>What this does to clients</strong>
              <ul className="list-disc pl-5">{plan.impact.map((i) => <li key={i}>{i}</li>)}</ul>
            </div>
            <ImpactPanel affected={plan.affected} />
            <Callout tone="info" title="Check outside the appliance">
              <p className="mb-1">The appliance cannot verify these.</p>
              <ul className="list-disc pl-5">{[...plan.switch_requirements, ...(plan.external_requirements ?? [])].map((i) => <li key={i}>{i}</li>)}</ul>
            </Callout>
          </div>
        )}
      </DialogForm>
    </SectionCard>
  );
}
