"use client";

import { useEffect, useState } from "react";
import { api, ListResp, Whoami, WalledGardenRule } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { formatRelative, errMsg } from "@/lib/utils";

function kindTone(kind: string): "info" | "default" | "warn" {
  switch (kind) {
    case "domain": return "info";
    case "cidr":   return "warn";
    default:       return "default";
  }
}

export default function WalledGardenPage() {
  const [rows, setRows] = useState<WalledGardenRule[] | null>(null);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  const writable = canWrite("walled-garden", roles);

  async function load() {
    try { setRows((await api.get<ListResp<WalledGardenRule>>("/walled-garden")).data); }
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
    const portsRaw = (form.get("ports") as string) || "";
    const ports = portsRaw.split(",").map((p) => p.trim()).filter(Boolean).map(Number);
    if (ports.some((p) => !Number.isInteger(p) || p < 1 || p > 65535)) {
      setErr("Ports must be integers 1..65535"); setBusy(false); return;
    }
    try {
      await api.post("/walled-garden", {
        kind: form.get("kind"),
        value: form.get("value"),
        ports: ports.length ? ports : undefined,
        description: (form.get("description") as string) || undefined,
      });
      setShowNew(false);
      (e.target as HTMLFormElement).reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onDelete(id: string) {
    if (!confirm("Delete this walled-garden rule?")) return;
    try { await api.del(`/walled-garden/${id}`); load(); }
    catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Site</div>
          <h1 className="text-2xl font-semibold">Walled garden</h1>
        </div>
        {writable && (
          <Button onClick={() => setShowNew((v) => !v)}>
            {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New rule</>}
          </Button>
        )}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && writable && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New rule</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-4 gap-3">
              <div>
                <Label>Kind</Label>
                <select name="kind" className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  <option value="domain">domain</option>
                  <option value="ip">ip</option>
                  <option value="cidr">cidr</option>
                </select>
              </div>
              <div><Label>Value</Label><Input name="value" required placeholder="example.com / 1.2.3.4 / 10.0.0.0/24" /></div>
              <div><Label>Ports (comma)</Label><Input name="ports" placeholder="80, 443 (blank = all)" /></div>
              <div><Label>Description</Label><Input name="description" placeholder="Optional" /></div>
              <div className="sm:col-span-4 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Adding…" : "Add rule"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No walled-garden rules" hint="Allow pre-auth access to captive-portal and payment endpoints." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Kind</TH><TH>Value</TH><TH>Ports</TH><TH>Description</TH><TH>Created</TH><TH></TH></TR>
              </THead>
              <tbody>
                {rows.map((r) => (
                  <TR key={r.id}>
                    <TD><Badge tone={kindTone(r.kind)}>{r.kind}</Badge></TD>
                    <TD className="font-mono">{r.value}</TD>
                    <TD className="text-muted font-mono text-xs">{r.ports && r.ports.length ? r.ports.join(", ") : "all"}</TD>
                    <TD className="text-muted">{r.description || "—"}</TD>
                    <TD className="text-muted">{formatRelative(r.created_at)}</TD>
                    <TD className="text-right">
                      {writable && <Button size="sm" variant="ghost" onClick={() => onDelete(r.id)}>Delete</Button>}
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
