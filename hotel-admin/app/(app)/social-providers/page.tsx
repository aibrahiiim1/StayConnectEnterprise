"use client";

import { useEffect, useState } from "react";
import { api, ListResp, Whoami, SocialOAuthProvider } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { formatRelative, errMsg } from "@/lib/utils";

const PROVIDERS = ["google", "apple", "facebook", "microsoft"] as const;

export default function SocialProvidersPage() {
  const [rows, setRows] = useState<SocialOAuthProvider[] | null>(null);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<SocialOAuthProvider | null>(null);

  const writable = canWrite("social-providers", roles);

  async function load() {
    try { setRows((await api.get<ListResp<SocialOAuthProvider>>("/social-providers")).data); }
    catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const s = (k: string) => ((form.get(k) as string) || undefined);
    try {
      await api.post("/social-providers", {
        provider: form.get("provider"),
        display_name: s("display_name"),
        client_id: form.get("client_id"),
        client_secret: form.get("client_secret"),
        redirect_uri: form.get("redirect_uri"),
        scopes: s("scopes"),
        enabled: form.get("enabled") === "on",
      });
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onEdit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!editing) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const body: Record<string, unknown> = {
      display_name: (form.get("display_name") as string) ?? "",
      client_id: (form.get("client_id") as string) || undefined,
      redirect_uri: (form.get("redirect_uri") as string) || undefined,
      scopes: (form.get("scopes") as string) ?? "",
      enabled: form.get("enabled") === "on",
    };
    const cs = form.get("client_secret") as string;
    if (cs) body.client_secret = cs; // blank keeps existing secret
    try {
      await api.patch(`/social-providers/${editing.id}`, body);
      setEditing(null);
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onDelete(id: string) {
    if (!confirm("Delete this social login provider?")) return;
    try { await api.del(`/social-providers/${id}`); load(); }
    catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Integrations</div>
          <h1 className="text-2xl font-semibold">Social login</h1>
        </div>
        {writable && (
          <Button onClick={() => { setShowNew((v) => !v); setEditing(null); }}>
            {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New provider</>}
          </Button>
        )}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && writable && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New social provider</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <Label>Provider</Label>
                <select name="provider" className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {PROVIDERS.map((p) => <option key={p} value={p}>{p}</option>)}
                </select>
              </div>
              <div><Label>Display name</Label><Input name="display_name" placeholder="Optional" /></div>
              <div><Label>Client ID</Label><Input name="client_id" required /></div>
              <div><Label>Client secret</Label><Input name="client_secret" type="password" required placeholder="write-only" /></div>
              <div><Label>Redirect URI</Label><Input name="redirect_uri" required placeholder="https://portal/callback" /></div>
              <div><Label>Scopes</Label><Input name="scopes" placeholder="openid email profile" /></div>
              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="enabled" defaultChecked /> Enabled</label>
              <div className="sm:col-span-2 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {editing && writable && (
        <Card className="mb-6">
          <CardHeader>
            <CardTitle>Edit {editing.provider}</CardTitle>
            <Button size="sm" variant="ghost" onClick={() => setEditing(null)}><X size={14} /></Button>
          </CardHeader>
          <CardBody>
            <form onSubmit={onEdit} className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div><Label>Display name</Label><Input name="display_name" defaultValue={editing.display_name ?? ""} /></div>
              <div><Label>Client ID</Label><Input name="client_id" defaultValue={editing.client_id} /></div>
              <div><Label>Client secret</Label><Input name="client_secret" type="password" placeholder="leave blank to keep" /></div>
              <div><Label>Redirect URI</Label><Input name="redirect_uri" defaultValue={editing.redirect_uri} /></div>
              <div><Label>Scopes</Label><Input name="scopes" defaultValue={editing.scopes ?? ""} /></div>
              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="enabled" defaultChecked={editing.enabled} /> Enabled</label>
              <div className="sm:col-span-2 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Save"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No social login providers" hint="Add OAuth apps to let guests sign in with Google, Apple, etc." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Provider</TH><TH>Client ID</TH><TH>Redirect URI</TH><TH>Last success</TH><TH>Enabled</TH><TH></TH></TR>
              </THead>
              <tbody>
                {rows.map((p) => (
                  <TR key={p.id}>
                    <TD>{p.display_name || p.provider}</TD>
                    <TD className="font-mono text-xs max-w-xs truncate" title={p.client_id}>{p.client_id}</TD>
                    <TD className="font-mono text-xs max-w-xs truncate" title={p.redirect_uri}>{p.redirect_uri}</TD>
                    <TD className="text-muted">{p.last_success_at ? formatRelative(p.last_success_at) : "—"}</TD>
                    <TD>{p.enabled ? <Badge tone="ok">on</Badge> : <Badge tone="default">off</Badge>}</TD>
                    <TD className="text-right space-x-2">
                      {writable && <Button size="sm" variant="ghost" onClick={() => { setEditing(p); setShowNew(false); }}>Edit</Button>}
                      {writable && <Button size="sm" variant="ghost" onClick={() => onDelete(p.id)}>Delete</Button>}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
