"use client";

import { useEffect, useState } from "react";
import { api, ListResp, NotificationProvider } from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, Trash2 } from "lucide-react";
import { formatRelative, errMsg } from "@/lib/utils";

// Per-channel kind menu — keeps the form coherent with the server's
// CHECK constraint and notifyAllowedKinds map.
const KINDS_BY_CHANNEL: Record<string, NotificationProvider["kind"][]> = {
  email: ["stub", "sendgrid", "ses"],
  sms: ["stub", "twilio"],
};

const healthTone = (p: NotificationProvider) => {
  if (!p.enabled) return "default";
  if (p.last_error_at && (!p.last_success_at || p.last_error_at > p.last_success_at)) return "err";
  if (p.last_success_at) return "ok";
  return "info";
};

export default function NotificationsPage() {
  const tenantID = useTenant();
  const [rows, setRows] = useState<NotificationProvider[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [channel, setChannel] = useState<"email" | "sms">("email");

  async function load() {
    if (!tenantID) return;
    try {
      const r = await api.get<ListResp<NotificationProvider>>(`/v1/notification-providers?tenant_id=${tenantID}`);
      setRows(r.data ?? []);
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const body: any = {
      channel: form.get("channel"),
      kind: form.get("kind"),
      display_name: (form.get("display_name") as string) || undefined,
      api_key: (form.get("api_key") as string) || undefined,
      api_user: (form.get("api_user") as string) || undefined,
      from_address: (form.get("from_address") as string) || undefined,
      from_name: (form.get("from_name") as string) || undefined,
      enabled: form.get("enabled") === "true",
    };
    try {
      await api.post(`/v1/notification-providers?tenant_id=${tenantID}`, body);
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onToggle(p: NotificationProvider) {
    if (!tenantID) return;
    try {
      await api.patch(`/v1/notification-providers/${p.id}?tenant_id=${tenantID}`, { enabled: !p.enabled });
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  async function onDelete(p: NotificationProvider) {
    if (!tenantID) return;
    if (!confirm(`Delete ${p.channel} provider "${p.display_name || p.kind}"?`)) return;
    try {
      await api.del(`/v1/notification-providers/${p.id}?tenant_id=${tenantID}`);
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Integrations</div>
          <h1 className="text-2xl font-semibold">Notifications</h1>
          <div className="text-xs text-muted mt-1">
            Email + SMS providers for OTP delivery. One enabled provider per channel; scd falls back to the in-process stub when no row exists.
          </div>
        </div>
        <Button onClick={() => setShowNew((s) => !s)}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New provider</>}
        </Button>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New notification provider</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <Label>Channel</Label>
                <select name="channel" defaultValue="email"
                  onChange={(e) => setChannel(e.target.value as "email" | "sms")}
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  <option value="email">email</option>
                  <option value="sms">sms</option>
                </select>
              </div>
              <div>
                <Label>Kind</Label>
                <select name="kind" defaultValue={KINDS_BY_CHANNEL[channel][0]}
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {KINDS_BY_CHANNEL[channel].map((k) => <option key={k} value={k}>{k}</option>)}
                </select>
              </div>
              <div>
                <Label>Display name</Label>
                <Input name="display_name" placeholder="Production SendGrid" />
              </div>
              <div className="sm:col-span-2">
                <Label>API key {channel === "sms" ? "(Twilio auth_token)" : "(secret — write-only)"}</Label>
                <Input name="api_key" type="password" placeholder="SG.xxxx / Twilio auth_token" />
              </div>
              {channel === "sms" && (
                <div>
                  <Label>API user (Twilio account SID)</Label>
                  <Input name="api_user" placeholder="ACxxxxxxxx..." />
                </div>
              )}
              {channel === "email" ? (
                <>
                  <div><Label>From address</Label><Input name="from_address" placeholder="noreply@hotel.com" /></div>
                  <div><Label>From name</Label><Input name="from_name" placeholder="Hotel WiFi" /></div>
                </>
              ) : (
                <div><Label>From number (E.164)</Label><Input name="from_address" placeholder="+15551234567" /></div>
              )}
              <div>
                <Label>Enabled</Label>
                <select name="enabled" defaultValue="true"
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  <option value="true">Enabled</option>
                  <option value="false">Disabled</option>
                </select>
              </div>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> :
           rows.length === 0 ? (
            <EmptyState title="No notification providers" hint="scd is using the in-process stub for both channels." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Channel</TH><TH>Kind</TH><TH>Name</TH><TH>From</TH>
                  <TH>Enabled</TH><TH>Health</TH><TH>Last activity</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((p) => (
                  <TR key={p.id}>
                    <TD className="font-mono text-xs">{p.channel}</TD>
                    <TD className="text-muted">{p.kind}</TD>
                    <TD>{p.display_name || "—"}</TD>
                    <TD className="font-mono text-xs">{p.from_address || "—"}</TD>
                    <TD>
                      <button onClick={() => onToggle(p)}
                        className={`text-xs px-2 py-0.5 rounded border ${p.enabled
                          ? "text-ok border-[#1e5c3c] bg-[#123422]"
                          : "text-muted border-border"}`}>
                        {p.enabled ? "enabled" : "disabled"}
                      </button>
                    </TD>
                    <TD>
                      {p.last_error_at && (!p.last_success_at || p.last_error_at > p.last_success_at) ? (
                        <span title={p.last_error}><Badge tone={healthTone(p) as any}>error</Badge></span>
                      ) : p.last_success_at ? (
                        <Badge tone={healthTone(p) as any}>ok</Badge>
                      ) : (
                        <Badge tone={healthTone(p) as any}>—</Badge>
                      )}
                    </TD>
                    <TD className="text-muted text-xs">
                      {p.last_success_at ? formatRelative(p.last_success_at) :
                       p.last_error_at ? formatRelative(p.last_error_at) : "—"}
                    </TD>
                    <TD className="text-right">
                      <Button size="sm" variant="ghost" onClick={() => onDelete(p)}><Trash2 size={12} /></Button>
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
