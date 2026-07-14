"use client";

import { useEffect, useState } from "react";
import { api, ListResp, Whoami, NotificationProvider } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { formatRelative, errMsg } from "@/lib/utils";

const KINDS: Record<string, string[]> = {
  email: ["stub", "sendgrid", "ses"],
  sms: ["stub", "twilio"],
};

function healthTone(n: NotificationProvider): "ok" | "err" | "default" {
  if (n.last_error_at && (!n.last_success_at || n.last_error_at > n.last_success_at)) return "err";
  if (n.last_success_at) return "ok";
  return "default";
}

export default function NotificationsPage() {
  const [rows, setRows] = useState<NotificationProvider[] | null>(null);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [channel, setChannel] = useState<"email" | "sms">("email");
  const [editing, setEditing] = useState<NotificationProvider | null>(null);

  const writable = canWrite("notification-providers", roles);

  async function load() {
    try { setRows((await api.get<ListResp<NotificationProvider>>("/notification-providers")).data); }
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
      await api.post("/notification-providers", {
        channel,
        kind: form.get("kind"),
        display_name: s("display_name"),
        api_key: s("api_key"),
        api_user: s("api_user"),
        from_address: s("from_address"),
        from_name: s("from_name"),
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
      api_user: (form.get("api_user") as string) ?? "",
      from_address: (form.get("from_address") as string) ?? "",
      from_name: (form.get("from_name") as string) ?? "",
      enabled: form.get("enabled") === "on",
    };
    const ak = form.get("api_key") as string;
    if (ak) body.api_key = ak; // blank keeps existing secret
    try {
      await api.patch(`/notification-providers/${editing.id}`, body);
      setEditing(null);
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onDelete(id: string) {
    if (!confirm("Delete this notification provider?")) return;
    try { await api.del(`/notification-providers/${id}`); load(); }
    catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Integrations</div>
          <h1 className="text-2xl font-semibold">Notifications</h1>
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
          <CardHeader><CardTitle>New notification provider</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <Label>Channel</Label>
                <select
                  name="channel" value={channel} onChange={(e) => setChannel(e.target.value as "email" | "sms")}
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
                >
                  <option value="email">email</option>
                  <option value="sms">sms</option>
                </select>
              </div>
              <div>
                <Label>Kind</Label>
                <select name="kind" className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {KINDS[channel].map((k) => <option key={k} value={k}>{k}</option>)}
                </select>
              </div>
              <div><Label>Display name</Label><Input name="display_name" placeholder="Optional" /></div>
              <div><Label>API key</Label><Input name="api_key" type="password" placeholder="write-only" /></div>
              <div><Label>API user {channel === "sms" ? "(Twilio SID)" : ""}</Label><Input name="api_user" placeholder="Optional" /></div>
              {channel === "email" && (
                <>
                  <div><Label>From address</Label><Input name="from_address" placeholder="noreply@hotel.com" /></div>
                  <div><Label>From name</Label><Input name="from_name" placeholder="Hotel WiFi" /></div>
                </>
              )}
              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="enabled" defaultChecked /> Enabled</label>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {editing && writable && (
        <Card className="mb-6">
          <CardHeader>
            <CardTitle>Edit {editing.channel}/{editing.kind}</CardTitle>
            <Button size="sm" variant="ghost" onClick={() => setEditing(null)}><X size={14} /></Button>
          </CardHeader>
          <CardBody>
            <form onSubmit={onEdit} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div><Label>Display name</Label><Input name="display_name" defaultValue={editing.display_name ?? ""} /></div>
              <div><Label>API key</Label><Input name="api_key" type="password" placeholder="leave blank to keep" /></div>
              <div><Label>API user</Label><Input name="api_user" defaultValue={editing.api_user ?? ""} /></div>
              {editing.channel === "email" && (
                <>
                  <div><Label>From address</Label><Input name="from_address" defaultValue={editing.from_address ?? ""} /></div>
                  <div><Label>From name</Label><Input name="from_name" defaultValue={editing.from_name ?? ""} /></div>
                </>
              )}
              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="enabled" defaultChecked={editing.enabled} /> Enabled</label>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Save"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No notification providers" hint="Add an email or SMS sender to deliver OTP codes." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Channel</TH><TH>Kind</TH><TH>Sender</TH><TH>Health</TH><TH>Enabled</TH><TH></TH></TR>
              </THead>
              <tbody>
                {rows.map((n) => (
                  <TR key={n.id}>
                    <TD>{n.display_name || n.channel}</TD>
                    <TD className="font-mono text-xs">{n.kind}</TD>
                    <TD className="text-muted text-xs">
                      {n.from_address || n.api_user || "—"}
                      {n.from_name && <div>{n.from_name}</div>}
                    </TD>
                    <TD>
                      <Badge tone={healthTone(n)}>
                        {healthTone(n) === "err" ? "error" : healthTone(n) === "ok" ? "ok" : "idle"}
                      </Badge>
                      {n.last_success_at && <div className="text-xs text-muted mt-1">{formatRelative(n.last_success_at)}</div>}
                      {n.last_error && <div className="text-xs text-err mt-1 max-w-xs truncate" title={n.last_error}>{n.last_error}</div>}
                    </TD>
                    <TD>{n.enabled ? <Badge tone="ok">on</Badge> : <Badge tone="default">off</Badge>}</TD>
                    <TD className="text-right space-x-2">
                      {writable && <Button size="sm" variant="ghost" onClick={() => { setEditing(n); setShowNew(false); }}>Edit</Button>}
                      {writable && <Button size="sm" variant="ghost" onClick={() => onDelete(n.id)}>Delete</Button>}
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
