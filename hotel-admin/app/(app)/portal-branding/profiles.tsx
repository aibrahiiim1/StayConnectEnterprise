"use client";

// Which portal design is being edited: the site default, or a named profile that some client networks show instead.
// A profile is appearance and wording only; sign-in methods, PMS routing and packages are set elsewhere.

import { useCallback, useEffect, useState } from "react";
import { Layers } from "lucide-react";
import { api, ListResp, GuestNetwork } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";
import { useToast } from "@/components/ui/toast";
import { errMsg } from "@/lib/utils";

type Profile = { id: string; name: string; network_ids: string[]; is_default: boolean };
type ProfileList = { default: Profile; profiles: Profile[] };

const BASE = "/portal-branding/profiles";

export function ProfileBar({ profile, onSelect, writable, dirty }: {
  profile: string | null;
  onSelect: (id: string | null) => void;
  writable: boolean;
  dirty: boolean;
}) {
  const toast = useToast();
  const [list, setList] = useState<ProfileList | null>(null);
  const [networks, setNetworks] = useState<GuestNetwork[]>([]);
  const [dialog, setDialog] = useState<null | "new" | "rename" | "delete" | "networks">(null);
  const [name, setName] = useState("");
  const [copyFrom, setCopyFrom] = useState("default");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  const reload = useCallback(async () => {
    try {
      const r = await api.get<ProfileList>(BASE);
      if (!r || !Array.isArray(r.profiles) || !r.default) throw new Error("unexpected answer");
      setList(r);
      setNetworks((await api.get<ListResp<GuestNetwork>>("/network/guest-networks")).data ?? []);
    } catch {
      setList(null);
    }
  }, []);
  useEffect(() => { void reload(); }, [reload]);

  if (!list) return null;
  const current = list.profiles.find((p) => p.id === profile) ?? null;
  const shown = current ?? list.default;

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
    toast.success(`Profile "${name.trim()}" created`, "It starts as a copy. Assign it to the networks that should show it.");
    onSelect(r.id);
  });
  const rename = () => run(async () => {
    await api.patch(`${BASE}/${profile}`, { name });
    toast.success("Profile renamed");
  });
  const remove = () => run(async () => {
    const r = await api.del<{ networks_returned_to_default: number }>(`${BASE}/${profile}`);
    toast.success("Profile deleted", `${r.networks_returned_to_default} network(s) now show the site default.`);
    onSelect(null);
  });
  const assign = (network: string, to: string) => run(async () => {
    await api.put(`${BASE}/assignments/${network}`, { profile_id: to === "default" ? null : to });
  }, true);

  const owner = (nid: string) => list.profiles.find((p) => p.network_ids.includes(nid))?.id ?? "default";
  const names = (ids: string[]) => ids.map((id) => networks.find((n) => n.id === id)?.name ?? "a network");

  return (
    <div className="flex flex-wrap items-end gap-3 rounded-lg border border-border bg-card p-3">
      <Field label="Portal design" htmlFor="portal-profile" className="min-w-48 flex-1">
        <Select id="portal-profile" value={profile ?? "default"} disabled={dirty}
          onChange={(e) => onSelect(e.target.value === "default" ? null : e.target.value)}>
          <option value="default">Site default</option>
          {list.profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </Select>
      </Field>
      <p className="min-w-48 flex-[2] text-sm text-muted-foreground">
        {dirty ? "Save or discard your changes to switch to another design." :
          shown.network_ids.length === 0
            ? (current ? "No client network shows this profile yet." : "No client network is using the site default.")
            : `Shown on: ${names(shown.network_ids).join(", ")}${current ? "" : " (every network without a profile of its own)"}.`}
      </p>
      {writable && (
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" size="sm" onClick={() => { setName(""); setCopyFrom(profile ?? "default"); setErr(null); setDialog("new"); }}>
            <Layers /> New profile…
          </Button>
          {current && <Button variant="secondary" size="sm" onClick={() => { setName(current.name); setErr(null); setDialog("rename"); }}>Rename…</Button>}
          {current && <Button variant="secondary" size="sm" onClick={() => { setErr(null); setDialog("delete"); }}>Delete…</Button>}
          <Button variant="secondary" size="sm" onClick={() => { setErr(null); setDialog("networks"); }}>Networks…</Button>
        </div>
      )}

      <DialogForm open={dialog === "new"} onOpenChange={(v) => !v && setDialog(null)} title="New portal profile"
        description="A profile is another sign-in page design. It starts as a copy of the design you choose." submitLabel="Create profile"
        busy={busy} error={err} disabled={name.trim() === ""} disabledReason="Give the profile a name." onSubmit={create}>
        <Field label="Name"><Input value={name} maxLength={80} onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="Start from">
          <Select value={copyFrom} onChange={(e) => setCopyFrom(e.target.value)}>
            <option value="default">Site default</option>
            {list.profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </Select>
        </Field>
      </DialogForm>

      <DialogForm open={dialog === "rename"} onOpenChange={(v) => !v && setDialog(null)} title="Rename profile" submitLabel="Rename"
        busy={busy} error={err} disabled={name.trim() === ""} disabledReason="Give the profile a name." onSubmit={rename}>
        <Field label="Name"><Input value={name} maxLength={80} onChange={(e) => setName(e.target.value)} /></Field>
      </DialogForm>

      <ConfirmDialog open={dialog === "delete"} onOpenChange={(v) => !v && setDialog(null)} title={`Delete "${current?.name ?? ""}"?`}
        description="Networks that show this profile go back to the site default. The profile's design and history are removed."
        confirmLabel="Delete profile" confirmVariant="danger" busy={busy} error={err} onConfirm={remove} />

      <DialogForm open={dialog === "networks"} onOpenChange={(v) => !v && setDialog(null)} title="Which design each network shows"
        description="Changes apply at once. A client sees the design of the network it is connected to." submitLabel="Done" cancelLabel="Close"
        busy={busy} error={err} onSubmit={() => setDialog(null)}>
        {networks.length === 0 && <p className="text-sm text-muted-foreground">There are no client networks yet.</p>}
        {networks.map((n) => (
          <Field key={n.id} label={n.name}>
            <Select value={owner(n.id)} disabled={busy} aria-label={`Portal design for ${n.name}`} onChange={(e) => void assign(n.id, e.target.value)}>
              <option value="default">Site default</option>
              {list.profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </Select>
          </Field>
        ))}
        {err ? <p className="text-sm text-danger">{errMsg(err)}</p> : null}
      </DialogForm>
    </div>
  );
}
