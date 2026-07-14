"use client";

import { useEffect, useState } from "react";
import { api, ListResp, SocialOAuthProvider } from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, Trash2 } from "lucide-react";
import { formatRelative, errMsg } from "@/lib/utils";

const PROVIDERS: SocialOAuthProvider["provider"][] = ["google", "apple", "facebook", "microsoft"];

const healthTone = (p: SocialOAuthProvider) => {
  if (!p.enabled) return "default";
  if (p.last_error_at && (!p.last_success_at || p.last_error_at > p.last_success_at)) return "err";
  if (p.last_success_at) return "ok";
  return "info";
};

export default function SocialProvidersPage() {
  const tenantID = useTenant();
  const [rows, setRows] = useState<SocialOAuthProvider[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    if (!tenantID) return;
    try {
      const r = await api.get<ListResp<SocialOAuthProvider>>(`/v1/social-providers?tenant_id=${tenantID}`);
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
      provider: form.get("provider"),
      display_name: (form.get("display_name") as string) || undefined,
      client_id: form.get("client_id"),
      client_secret: form.get("client_secret"),
      redirect_uri: form.get("redirect_uri"),
      scopes: (form.get("scopes") as string) || undefined,
      enabled: form.get("enabled") === "true",
    };
    try {
      await api.post(`/v1/social-providers?tenant_id=${tenantID}`, body);
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onToggle(p: SocialOAuthProvider) {
    if (!tenantID) return;
    try {
      await api.patch(`/v1/social-providers/${p.id}?tenant_id=${tenantID}`, { enabled: !p.enabled });
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  async function onDelete(p: SocialOAuthProvider) {
    if (!tenantID) return;
    if (!confirm(`Delete ${p.provider} provider "${p.display_name || p.client_id.slice(0, 12)}…"?`)) return;
    try {
      await api.del(`/v1/social-providers/${p.id}?tenant_id=${tenantID}`);
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Integrations</div>
          <h1 className="text-2xl font-semibold">Social login (OAuth)</h1>
          <div className="text-xs text-muted mt-1">
            Per-tenant OAuth client credentials. scd uses the in-process stub for any provider without a real row — useful for dev.
          </div>
        </div>
        <Button onClick={() => setShowNew((s) => !s)}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New provider</>}
        </Button>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New social provider</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <Label>Provider</Label>
                <select name="provider" defaultValue="google"
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {PROVIDERS.map((p) => <option key={p} value={p}>{p}</option>)}
                </select>
              </div>
              <div className="sm:col-span-2">
                <Label>Display name</Label>
                <Input name="display_name" placeholder="Production Google" />
              </div>
              <div className="sm:col-span-3">
                <Label>Client ID</Label>
                <Input name="client_id" required placeholder="1234567890-abcdef.apps.googleusercontent.com" />
              </div>
              <div className="sm:col-span-3">
                <Label>Client secret (write-only)</Label>
                <Input name="client_secret" type="password" required placeholder="GOCSPX-…" />
              </div>
              <div className="sm:col-span-2">
                <Label>Redirect URI</Label>
                <Input name="redirect_uri" required placeholder="https://portal.hotel.com/auth/social/callback" />
              </div>
              <div>
                <Label>Scopes (optional)</Label>
                <Input name="scopes" placeholder="openid email profile" />
              </div>
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
            <EmptyState title="No social providers" hint="scd is using the in-process stub for any 'social/start' calls." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Provider</TH><TH>Name</TH><TH>Client ID</TH>
                  <TH>Enabled</TH><TH>Health</TH><TH>Last activity</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((p) => (
                  <TR key={p.id}>
                    <TD className="font-mono text-xs">{p.provider}</TD>
                    <TD>{p.display_name || "—"}</TD>
                    <TD className="font-mono text-xs truncate max-w-[280px]" title={p.client_id}>{p.client_id}</TD>
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
