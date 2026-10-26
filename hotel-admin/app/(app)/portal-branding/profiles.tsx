"use client";

// Portal designs and where each one is shown.
//
// A design is appearance and wording only. The site default is the design every network shows unless it has been given one of its own (a "profile"); a profile can be
// shown on any number of networks. Which sign-in methods a network offers, which PMS room sign-in uses and which packages apply are set elsewhere and are not touched here.
// Only networks that are serving clients are offered a choice; switched-off and archived networks keep their assignment and are listed apart, read-only.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { ArrowRight, Layers, Pencil } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Callout } from "@/components/ui/error-banner";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";
import { useToast } from "@/components/ui/toast";
import { cn, errMsg } from "@/lib/utils";
import { SERVICE_STATE_WORDS, splitByState, topologyWords, type ServiceState } from "@/lib/network-state";

type Profile = { id: string; name: string; network_ids: string[]; is_default: boolean };
type DesignNetwork = {
  id: string; name: string; service_state: ServiceState; network_type: string; vlan_id?: number; ssid_label?: string; parent_interface: string;
  /** "default" or a profile id. */
  profile_id: string;
};
type ProfileList = { default: Profile; profiles: Profile[]; networks?: DesignNetwork[] };

const BASE = "/portal-branding/profiles";
const DEFAULT = "default";

export function ProfileBar({ profile, onSelect, writable, dirty }: {
  profile: string | null;
  onSelect: (id: string | null) => void;
  writable: boolean;
  dirty: boolean;
}) {
  const toast = useToast();
  const [list, setList] = useState<ProfileList | null>(null);
  const [dialog, setDialog] = useState<null | "new" | "rename" | "delete" | "apply">(null);
  const [name, setName] = useState("");
  const [copyFrom, setCopyFrom] = useState(DEFAULT);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  // Choices not yet applied: network id -> design id. Nothing reaches clients until Apply.
  const [pending, setPending] = useState<Record<string, string>>({});
  const [showOthers, setShowOthers] = useState(false);

  const reload = useCallback(async () => {
    try {
      const r = await api.get<ProfileList>(BASE);
      if (!r || !Array.isArray(r.profiles) || !r.default) throw new Error("unexpected answer");
      setList(r);
    } catch {
      setList(null);
    }
  }, []);
  useEffect(() => { void reload(); }, [reload]);

  const networks = useMemo(() => list?.networks ?? [], [list]);
  const groups = useMemo(() => splitByState(networks), [networks]);
  const designName = useCallback((id: string) => id === DEFAULT ? "Site default" : list?.profiles.find((p) => p.id === id)?.name ?? "a deleted design", [list]);

  if (!list) return null;
  const current = list.profiles.find((p) => p.id === profile) ?? null;
  const editingId = current ? current.id : DEFAULT;
  const changes = Object.entries(pending).filter(([nid, to]) => networks.find((n) => n.id === nid)?.profile_id !== to);
  const servingOf = (id: string) => groups.serving.filter((n) => (pending[n.id] ?? n.profile_id) === id);
  const inactiveOf = (id: string) => [...groups.off, ...groups.archived].filter((n) => n.profile_id === id);

  async function run(fn: () => Promise<void>, keepOpen = false) {
    setBusy(true);
    setErr(null);
    try {
      await fn();
      if (!keepOpen) setDialog(null);
      await reload();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  const create = () => run(async () => {
    const r = await api.post<{ id: string }>(BASE, { name, copy_from: copyFrom });
    toast.success(`Design "${name.trim()}" created`, "It starts as a copy. Choose the networks that should show it.");
    onSelect(r.id);
  });
  const rename = () => run(async () => {
    await api.patch(`${BASE}/${profile}`, { name });
    toast.success("Design renamed");
  });
  const remove = () => run(async () => {
    const r = await api.del<{ networks_returned_to_default: number }>(`${BASE}/${profile}`);
    toast.success("Design deleted", `${r.networks_returned_to_default} network(s) now show the site default.`);
    onSelect(null);
  });
  const apply = () => run(async () => {
    for (const [nid, to] of changes) {
      await api.put(`${BASE}/assignments/${nid}`, { profile_id: to === DEFAULT ? null : to });
    }
    toast.success(`${changes.length} network${changes.length === 1 ? "" : "s"} updated`, "Clients see the new design on their next visit to the sign-in page.");
    setPending({});
  });

  const choose = (nid: string, to: string) => setPending((p) => {
    const next = { ...p, [nid]: to };
    if (networks.find((n) => n.id === nid)?.profile_id === to) delete next[nid];
    return next;
  });

  const designs: { id: string; name: string; isDefault: boolean }[] = [
    { id: DEFAULT, name: "Site default", isDefault: true },
    ...list.profiles.map((p) => ({ id: p.id, name: p.name, isDefault: false })),
  ];

  return (
    <Card>
      <CardBody className="space-y-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 className="text-base font-semibold">Portal designs</h2>
            <p className="mt-0.5 text-sm text-muted-foreground">
              A design is how the sign-in page looks and what it says. Pick the design to edit, and choose which networks show which design.
            </p>
          </div>
          {writable && (
            <Button variant="secondary" size="sm" onClick={() => { setName(""); setCopyFrom(editingId); setErr(null); setDialog("new"); }}>
              <Layers /> New design…
            </Button>
          )}
        </div>

        <ul className="grid gap-3 md:grid-cols-2 xl:grid-cols-3" aria-label="Designs">
          {designs.map((d) => {
            const serving = servingOf(d.id);
            const inactive = inactiveOf(d.id);
            const editing = d.id === editingId;
            return (
              <li key={d.id} className={cn("flex flex-col gap-2 rounded-lg border p-3", editing ? "border-primary bg-primary/5" : "border-border")}>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{d.name}</span>
                  {d.isDefault && <Badge tone="accent" size="sm">Every network without its own</Badge>}
                  {editing && <Badge tone="info" size="sm">Editing below</Badge>}
                </div>
                <p className="text-sm text-muted-foreground">
                  {serving.length === 0
                    ? (d.isDefault ? "No serving network uses the site default." : "No serving network shows this design yet.")
                    : <>Shown on <strong className="font-medium text-foreground">{serving.length}</strong> network{serving.length === 1 ? "" : "s"}: {serving.map((n) => n.name).join(", ")}</>}
                </p>
                {inactive.length > 0 && (
                  <p className="text-xs text-muted-foreground">
                    Also assigned to {inactive.length} switched-off or archived network{inactive.length === 1 ? "" : "s"}; it takes effect if {inactive.length === 1 ? "it is" : "they are"} switched on.
                  </p>
                )}
                <div className="mt-auto flex flex-wrap gap-2 pt-1">
                  <Button size="sm" variant={editing ? "ghost" : "secondary"} disabled={dirty || editing} onClick={() => onSelect(d.isDefault ? null : d.id)}>
                    <Pencil /> {editing ? "Editing" : "Edit this design"}
                  </Button>
                  {writable && editing && !d.isDefault && (
                    <>
                      <Button size="sm" variant="ghost" onClick={() => { setName(current?.name ?? ""); setErr(null); setDialog("rename"); }}>Rename…</Button>
                      <Button size="sm" variant="ghost" onClick={() => { setErr(null); setDialog("delete"); }}>Delete…</Button>
                    </>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
        {dirty && <p className="text-sm text-muted-foreground">Save or discard your changes to the open design to switch to another one.</p>}

        <div>
          <h3 className="text-sm font-semibold">Which design each network shows</h3>
          <p className="mt-0.5 text-sm text-muted-foreground">
            Only networks serving clients are listed. A client sees the design of the network it is connected to. Nothing changes for clients until you apply.
          </p>
          {groups.serving.length === 0 ? (
            <p className="mt-3 text-sm text-muted-foreground">
              No client network is serving clients yet. <Link href="/network" className="font-medium text-primary underline-offset-2 hover:underline">Set one up under Client networks</Link>.
            </p>
          ) : (
            <ul className="mt-3 divide-y divide-border rounded-lg border border-border">
              {groups.serving.map((n) => {
                const topo = topologyWords(n);
                const shows = pending[n.id] ?? n.profile_id;
                const edited = pending[n.id] !== undefined && pending[n.id] !== n.profile_id;
                return (
                  <li key={n.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5">
                    <div className="min-w-44 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-medium">{n.name}</span>
                        <Badge tone="neutral" size="sm" title={topo.detail}>{topo.label}</Badge>
                      </div>
                      {n.ssid_label && <div className="text-xs text-muted-foreground">Wi-Fi name {n.ssid_label}</div>}
                    </div>
                    <div className="flex min-w-56 flex-1 flex-wrap items-center gap-2">
                      <Select aria-label={`Design shown on ${n.name}`} value={shows} disabled={!writable || busy} className="min-w-44 flex-1"
                        onChange={(e) => choose(n.id, e.target.value)}>
                        {designs.map((d) => <option key={d.id} value={d.id}>{d.isDefault ? "Site default" : d.name}</option>)}
                      </Select>
                      {edited && <Badge tone="warn" size="sm">Not applied</Badge>}
                    </div>
                    <div className="text-sm text-muted-foreground sm:w-56">
                      Clients see <strong className="font-medium text-foreground">{designName(shows)}</strong>
                      {shows !== editingId && !dirty && (
                        <button type="button" className="ms-2 inline-flex items-center gap-1 text-primary underline-offset-2 hover:underline"
                          onClick={() => onSelect(shows === DEFAULT ? null : shows)}>
                          Preview <ArrowRight className="size-3" aria-hidden />
                        </button>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
          )}

          {writable && changes.length > 0 && (
            <div className="mt-3 flex flex-wrap items-center gap-3 rounded-lg border border-warning/40 bg-warning-subtle px-3 py-2.5 text-sm">
              <span className="flex-1">
                {changes.length} change{changes.length === 1 ? "" : "s"} not applied yet:{" "}
                {changes.map(([nid, to]) => `${networks.find((n) => n.id === nid)?.name}: ${designName(networks.find((n) => n.id === nid)?.profile_id ?? DEFAULT)} → ${designName(to)}`).join("; ")}
              </span>
              <Button variant="ghost" size="sm" onClick={() => setPending({})}>Discard</Button>
              <Button size="sm" onClick={() => { setErr(null); setDialog("apply"); }}>Apply…</Button>
            </div>
          )}

          {(groups.off.length > 0 || groups.archived.length > 0) && (
            <div className="mt-3">
              <button type="button" aria-expanded={showOthers} onClick={() => setShowOthers((v) => !v)}
                className="text-sm font-medium text-primary underline-offset-2 hover:underline">
                {showOthers ? "Hide" : "Show"} {groups.off.length + groups.archived.length} switched-off or archived network{groups.off.length + groups.archived.length === 1 ? "" : "s"}
              </button>
              {showOthers && (
                <ul className="mt-2 divide-y divide-border rounded-lg border border-dashed border-border text-sm">
                  {[...groups.off, ...groups.archived].map((n) => (
                    <li key={n.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-muted-foreground">
                      <span className="font-medium text-foreground">{n.name}</span>
                      <Badge tone={SERVICE_STATE_WORDS[n.service_state].tone} size="sm">{SERVICE_STATE_WORDS[n.service_state].label}</Badge>
                      <span>keeps {designName(n.profile_id)}</span>
                      <Link href={n.service_state === "archived" ? "/network" : `/network/${n.id}`} className="ms-auto text-primary underline-offset-2 hover:underline">
                        {n.service_state === "archived" ? "Restore from the archive" : "Open the network"}
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>

        <Callout tone="neutral" title="This page only changes how the sign-in page looks">
          Which sign-in methods a network offers is set under <Link href="/sign-in-methods" className="underline underline-offset-2">Sign-in methods</Link>,
          which PMS serves room sign-in under <Link href="/pms-routing" className="underline underline-offset-2">PMS routing</Link>, and who can get which package under{" "}
          <Link href="/internet-packages" className="underline underline-offset-2">Internet packages</Link>. A design never changes any of them.
        </Callout>
      </CardBody>

      <DialogForm open={dialog === "new"} onOpenChange={(v) => !v && setDialog(null)} title="New portal design"
        description="A design is another sign-in page look. It starts as a copy of the design you choose." submitLabel="Create design"
        busy={busy} error={err} disabled={name.trim() === ""} disabledReason="Give the design a name." onSubmit={create}>
        <Field label="Name"><Input value={name} maxLength={80} onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="Start from">
          <Select value={copyFrom} onChange={(e) => setCopyFrom(e.target.value)}>
            {designs.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
          </Select>
        </Field>
      </DialogForm>

      <DialogForm open={dialog === "rename"} onOpenChange={(v) => !v && setDialog(null)} title="Rename design" submitLabel="Rename"
        busy={busy} error={err} disabled={name.trim() === ""} disabledReason="Give the design a name." onSubmit={rename}>
        <Field label="Name"><Input value={name} maxLength={80} onChange={(e) => setName(e.target.value)} /></Field>
      </DialogForm>

      <ConfirmDialog open={dialog === "delete"} onOpenChange={(v) => !v && setDialog(null)} title={`Delete "${current?.name ?? ""}"?`}
        description="Every network that shows this design goes back to the site default. The design and its history are removed."
        consequences={[
          `${servingOf(editingId).length} serving network(s) switch to the site default straight away.`,
          `${inactiveOf(editingId).length} switched-off or archived network(s) also fall back to the site default.`,
        ]}
        confirmLabel="Delete design" confirmVariant="danger" busy={busy} error={err} onConfirm={remove} />

      <ConfirmDialog open={dialog === "apply"} onOpenChange={(v) => !v && setDialog(null)} title="Change which design these networks show?"
        description="Clients see the new design the next time they open the sign-in page. Their sign-in methods, PMS and packages do not change."
        consequences={changes.map(([nid, to]) => {
          const n = networks.find((x) => x.id === nid);
          return `${n?.name}: ${designName(n?.profile_id ?? DEFAULT)} → ${designName(to)}`;
        })}
        confirmLabel={`Apply ${changes.length} change${changes.length === 1 ? "" : "s"}`} busy={busy} error={err ? errMsg(err) : null} onConfirm={apply} />
    </Card>
  );
}
