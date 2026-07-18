"use client";

// Phase 2 (DARK) Hotel-Admin commercial-packages management. This page is only linked from the nav when
// NEXT_PUBLIC_PHASE2_ADMIN=1, and the edged routes are the authority: while the backend Phase-2 admin
// flag is OFF the API returns 503 and this page renders a clear "not enabled" state. It publishes
// immutable, free-only, non-PMS package revisions (validated server-side) and toggles activation.

import { useEffect, useState } from "react";
import { api, ApiError, ListResp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";

type PackageSummary = {
  package_id: string;
  code: string;
  active: boolean;
  current_revision_id: string;
  revision_count: number;
};

export default function CommercialPackagesPage() {
  const [rows, setRows] = useState<PackageSummary[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [disabled, setDisabled] = useState(false);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    setErr(null);
    try {
      const r = await api.get<ListResp<PackageSummary>>("/commercial-packages");
      setRows(r.data ?? []);
      setDisabled(false);
    } catch (e: any) {
      if (e instanceof ApiError && e.status === 503) {
        setDisabled(true);
        setRows([]);
        return;
      }
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, []);

  async function onPublish(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setErr(null);
    const el = e.currentTarget;
    const form = new FormData(el);
    const str = (k: string) => ((form.get(k) as string) || "").trim();
    const num = (k: string) => {
      const v = str(k);
      return v === "" ? undefined : Number(v);
    };
    // one grant tier; optional bandwidth overrides. Server re-validates every field.
    const grant: Record<string, number> = {};
    const dk = num("down_kbps"); if (dk !== undefined) grant.down_kbps = dk;
    const uk = num("up_kbps");   if (uk !== undefined) grant.up_kbps = uk;

    const endMode = str("end_mode") || "MANUAL_END";
    const duration: Record<string, unknown> = { end_mode: endMode };
    if (endMode === "VALIDITY_WINDOW") {
      const secs = num("duration_seconds");
      if (secs !== undefined) duration.duration_seconds = secs;
    }
    // optional auth-method eligibility rule
    const methods = str("methods");
    const rules = methods
      ? [{ type: "AUTH_METHOD", value: { methods: methods.split(",").map((m) => m.trim()).filter(Boolean) } }]
      : [];
    try {
      await api.post("/commercial-packages", {
        code: str("code"),
        service_plan_revision_id: str("service_plan_revision_id"),
        display: { name: str("name") || str("code") },
        duration_policy: duration,
        eligibility_rules: rules,
        grant_tiers: [{ order: 10, grant }],
      });
      el.reset();
      setShowNew(false);
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "Publish failed");
    } finally {
      setBusy(false);
    }
  }

  async function toggleActive(p: PackageSummary) {
    setBusy(true); setErr(null);
    try {
      await api.post(`/commercial-packages/${p.package_id}/active`, { active: !p.active });
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "Update failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Commercial packages</h1>
        {!disabled && (
          <Button onClick={() => setShowNew((v) => !v)}>
            {showNew ? <X size={16} /> : <Plus size={16} />}
            {showNew ? "Cancel" : "Publish package"}
          </Button>
        )}
      </div>

      {err && <div className="text-sm text-red-500">{err}</div>}

      {disabled && (
        <Card>
          <CardBody>
            <EmptyState
              title="Commercial packages are not enabled"
              hint="This is a Phase 2 (dark) feature. It is disabled on this appliance until the Phase-2 admin flag is turned on by an operator during a controlled cutover."
            />
          </CardBody>
        </Card>
      )}

      {!disabled && showNew && (
        <Card>
          <CardHeader><CardTitle>Publish a free package revision</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onPublish} className="grid grid-cols-2 gap-3">
              <div><Label>Code</Label><Input name="code" required placeholder="FREEWIFI" /></div>
              <div><Label>Display name</Label><Input name="name" placeholder="Free WiFi" /></div>
              <div className="col-span-2">
                <Label>Service plan revision ID</Label>
                <Input name="service_plan_revision_id" required placeholder="uuid of the plan revision to grant" />
              </div>
              <div>
                <Label>End mode</Label>
                <select name="end_mode" className="w-full bg-panel2 border border-border rounded-md px-2 py-2 text-sm">
                  <option value="MANUAL_END">Manual end</option>
                  <option value="VALIDITY_WINDOW">Validity window</option>
                </select>
              </div>
              <div><Label>Duration seconds (validity window)</Label><Input name="duration_seconds" type="number" min={1} placeholder="3600" /></div>
              <div><Label>Down kbps (optional)</Label><Input name="down_kbps" type="number" min={0} /></div>
              <div><Label>Up kbps (optional)</Label><Input name="up_kbps" type="number" min={0} /></div>
              <div className="col-span-2">
                <Label>Auth methods (optional, comma-separated)</Label>
                <Input name="methods" placeholder="account, voucher" />
              </div>
              <div className="col-span-2">
                <Button type="submit" disabled={busy}>{busy ? "Publishing…" : "Publish"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {!disabled && (
        <Card>
          <CardBody>
            {rows && rows.length === 0 ? (
              <EmptyState title="No packages" hint="Publish a package to create its first immutable revision." />
            ) : (
              <Table>
                <THead>
                  <TR><TH>Code</TH><TH>Status</TH><TH>Revisions</TH><TH>Current revision</TH><TH></TH></TR>
                </THead>
                <tbody>
                  {(rows ?? []).map((p) => (
                    <TR key={p.package_id}>
                      <TD className="font-medium">{p.code}</TD>
                      <TD>{p.active ? <Badge tone="ok">Active</Badge> : <Badge tone="default">Inactive</Badge>}</TD>
                      <TD>{p.revision_count}</TD>
                      <TD className="font-mono text-xs text-muted">{p.current_revision_id || "—"}</TD>
                      <TD>
                        <Button variant="ghost" disabled={busy} onClick={() => toggleActive(p)}>
                          {p.active ? "Deactivate" : "Activate"}
                        </Button>
                      </TD>
                    </TR>
                  ))}
                </tbody>
              </Table>
            )}
          </CardBody>
        </Card>
      )}
    </div>
  );
}
