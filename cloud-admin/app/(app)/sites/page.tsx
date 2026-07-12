"use client";

import { useEffect, useState } from "react";
import { api, ApiError, ListResp, Site, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { formatRelative } from "@/lib/utils";

function useTenant() {
  const [id, setId] = useState<string | null>(null);
  useEffect(() => {
    api.get<Whoami>("/v1/auth/whoami").then(async (w) => {
      if (w.default_tenant_id) return setId(w.default_tenant_id);
      const ts = await api.get<{ data: { id: string; slug: string }[] }>("/v1/tenants");
      setId(ts.data.find((t) => t.slug === "dev")?.id ?? ts.data[0]?.id ?? null);
    });
  }, []);
  return id;
}

export default function SitesPage() {
  const tenantID = useTenant();
  const [rows, setRows] = useState<Site[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    if (!tenantID) return;
    try {
      const r = await api.get<ListResp<Site>>(`/v1/sites?tenant_id=${tenantID}`);
      setRows(r.data);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    // Capture the element NOW: after the first await React has already nulled
    // e.currentTarget, so touching it later throws "reading 'reset' of null" and
    // surfaces a bogus error even though the create succeeded.
    const el = e.currentTarget;
    try {
      await api.post(`/v1/sites?tenant_id=${tenantID}`, {
        code: form.get("code"),
        name: form.get("name"),
        timezone: form.get("timezone") || "UTC",
        country: form.get("country") || undefined,
      });
      setShowNew(false);
      el.reset();
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.body?.error === "limit_exceeded") {
        setErr(`Plan limit reached: ${e.body.limit_key} (${e.body.current}/${e.body.limit})`);
      } else {
        setErr(e?.message ?? "Create failed");
      }
    } finally {
      setBusy(false);
    }
  }

  async function onDelete(id: string) {
    if (!tenantID) return;
    if (!confirm("Delete this site? Appliances under it will cascade.")) return;
    try {
      await api.del(`/v1/sites/${id}?tenant_id=${tenantID}`);
      load();
    } catch (e: any) {
      setErr(e?.message ?? "Delete failed");
    }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Infrastructure</div>
          <h1 className="text-2xl font-semibold">Sites</h1>
        </div>
        <Button onClick={() => setShowNew((s) => !s)}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New site</>}
        </Button>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New site</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-4 gap-3">
              <div><Label>Code</Label><Input name="code" required placeholder="hq" /></div>
              <div><Label>Name</Label><Input name="name" required placeholder="Headquarters" /></div>
              <div><Label>Timezone</Label><Input name="timezone" placeholder="UTC" /></div>
              <div><Label>Country</Label><Input name="country" placeholder="US" /></div>
              <div className="sm:col-span-4 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <EmptyState title="Loading…" />
          ) : rows.length === 0 ? (
            <EmptyState title="No sites yet" hint="Create one to start managing appliances." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Code</TH><TH>Name</TH><TH>Timezone</TH><TH>Country</TH>
                  <TH>Created</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((s) => (
                  <TR key={s.id}>
                    <TD className="font-mono">{s.code}</TD>
                    <TD>{s.name}</TD>
                    <TD className="text-muted">{s.timezone}</TD>
                    <TD className="text-muted">{s.country || "—"}</TD>
                    <TD className="text-muted">{formatRelative(s.created_at)}</TD>
                    <TD className="text-right">
                      <Button size="sm" variant="ghost" onClick={() => onDelete(s.id)}>Delete</Button>
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
