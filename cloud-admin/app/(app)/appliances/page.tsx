"use client";

import { useEffect, useState } from "react";
import {
  api, ApiError, Appliance, ListResp, Site,
  BootstrapToken, BootstrapTokenCreated, EffectiveConfig,
} from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, Key, Copy, Trash2, Eye } from "lucide-react";
import { formatRelative, errMsg } from "@/lib/utils";

const toneFor = (status: string) =>
  status === "online" ? "ok" :
  status === "enrolled" ? "info" :
  status === "pending" ? "info" :
  status === "retired" ? "default" : "warn";

// LivePulse renders a small colored dot — solid green for fresh online,
// amber when last_seen is older than ~30s (about to be flipped to offline
// by the sweeper), grey otherwise.
function LivePulse({ status, lastSeen }: { status: string; lastSeen?: string }) {
  if (status !== "online" || !lastSeen) return null;
  const ageMs = Date.now() - new Date(lastSeen).getTime();
  const stale = ageMs > 25_000; // ahead of the 30s sweeper threshold
  const color = stale ? "bg-warn" : "bg-ok";
  return (
    <span title={`last beat ${Math.round(ageMs / 1000)}s ago`}
          className={`inline-block w-2 h-2 rounded-full mr-1 ${color} ${stale ? "" : "animate-pulse"}`} />
  );
}

export default function AppliancesPage() {
  const tenantID = useTenant();
  const [rows, setRows] = useState<Appliance[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [tokens, setTokens] = useState<BootstrapToken[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [showMint, setShowMint] = useState(false);
  const [mintedToken, setMintedToken] = useState<BootstrapTokenCreated | null>(null);
  const [busy, setBusy] = useState(false);

  // Effective-config drawer (5.7.D).
  const [effOpen, setEffOpen] = useState<Appliance | null>(null);
  const [effData, setEffData] = useState<EffectiveConfig | null>(null);

  // Re-render every 10s so the LivePulse component reflects updated
  // last_seen ages without a fresh API roundtrip.
  const [, setTick] = useState(0);
  useEffect(() => {
    const i = setInterval(() => setTick((n) => n + 1), 10_000);
    return () => clearInterval(i);
  }, []);

  async function load() {
    if (!tenantID) return;
    try {
      const [apps, st, tk] = await Promise.all([
        api.get<ListResp<Appliance>>(`/v1/appliances?tenant_id=${tenantID}`),
        api.get<ListResp<Site>>(`/v1/sites?tenant_id=${tenantID}`),
        api.get<ListResp<BootstrapToken>>(`/v1/appliance-bootstrap-tokens?tenant_id=${tenantID}`),
      ]);
      setRows(apps.data);
      setSites(st.data);
      setTokens(tk.data ?? []);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onMintToken(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    // Capture before awaiting: React nulls e.currentTarget after the first await,
    // so a later .reset() throws and shows a bogus error on a successful action.
    const el = e.currentTarget;
    try {
      const res = await api.post<BootstrapTokenCreated>(
        `/v1/appliance-bootstrap-tokens?tenant_id=${tenantID}`,
        {
          site_id: form.get("site_id"),
          expected_serial: (form.get("expected_serial") as string) || undefined,
          ttl_hours: Number(form.get("ttl_hours")) || 24,
        },
      );
      setMintedToken(res);
      setShowMint(false);
      el.reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onRevokeToken(id: string) {
    if (!tenantID) return;
    if (!confirm("Revoke this bootstrap token?")) return;
    try {
      await api.del(`/v1/appliance-bootstrap-tokens/${id}?tenant_id=${tenantID}`);
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  async function onShowEffective(a: Appliance) {
    if (!tenantID) return;
    setEffOpen(a);
    setEffData(null);
    try {
      const res = await api.get<EffectiveConfig>(
        `/v1/appliances/${a.id}/effective-config?tenant_id=${tenantID}`,
      );
      setEffData(res);
    } catch (e) { setErr(errMsg(e)); setEffOpen(null); }
  }


  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    // Capture before awaiting: React nulls e.currentTarget after the first await,
    // so a later .reset() throws and shows a bogus error on a successful action.
    const el = e.currentTarget;
    try {
      await api.post(`/v1/appliances?tenant_id=${tenantID}`, {
        site_id: form.get("site_id"),
        serial: form.get("serial"),
        name: form.get("name"),
        model: (form.get("model") as string) || undefined,
      });
      setShowNew(false);
      el.reset();
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.body?.error === "limit_exceeded") {
        setErr(`License limit reached: ${e.body.limit_key} (${e.body.current}/${e.body.limit})`);
      } else setErr(e?.message ?? "Create failed");
    } finally { setBusy(false); }
  }

  async function onDelete(id: string) {
    if (!tenantID) return;
    if (!confirm("Delete this appliance?")) return;
    try {
      await api.del(`/v1/appliances/${id}?tenant_id=${tenantID}`);
      load();
    } catch (e: any) { setErr(e?.message ?? "Delete failed"); }
  }

  const siteName = (sid: string) => sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Infrastructure</div>
          <h1 className="text-2xl font-semibold">Appliances</h1>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={() => { setShowMint((s) => !s); setShowNew(false); }} disabled={sites.length === 0}>
            {showMint ? <><X size={14} /> Cancel</> : <><Key size={14} /> Enrollment token</>}
          </Button>
          <Button onClick={() => { setShowNew((s) => !s); setShowMint(false); }} disabled={sites.length === 0}>
            {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New appliance</>}
          </Button>
        </div>
      </div>

      <p className="text-sm text-muted mb-4">
        Most appliances install <strong>zero-touch</strong>: a factory-clean box with internet self-registers and
        appears under <a href="/onboarding" className="text-brand hover:underline">Onboarding</a> as
        <em> Pending activation</em>, where you pick its customer, site and license terms and click Activate — no token.
        Mint an <strong>enrollment token</strong> below only for the advanced / manual install path (pre-registering a
        box, or an offline installer who will type a code).
      </p>
      {sites.length === 0 && (
        <div className="text-sm text-warn mb-4">
          Create a site first — appliances belong to a site.
        </div>
      )}
      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {mintedToken && (
        <Card className="mb-6 border-ok">
          <CardHeader>
            <CardTitle className="text-ok">New enrollment token — copy it now</CardTitle>
          </CardHeader>
          <CardBody>
            <div className="text-sm text-muted mb-2">
              This is the only time the full token is shown. Enter it in the appliance&apos;s{" "}
              <span className="font-mono">Hotel Admin → /setup/enrollment</span> wizard. Never edit files on
              the appliance: a factory unit has no customer identity and adopts its tenant/site only from the
              signed assignment you issue after claiming it.
            </div>
            <div className="flex items-center gap-2">
              <code className="flex-1 bg-panel2 border border-border rounded px-3 py-2 font-mono text-sm break-all">
                {mintedToken.token}
              </code>
              <Button size="sm" variant="secondary"
                onClick={() => navigator.clipboard?.writeText(mintedToken.token)}>
                <Copy size={12} /> Copy
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setMintedToken(null)}>
                <X size={14} /> Dismiss
              </Button>
            </div>
            <div className="text-xs text-muted mt-3">
              Site: {siteName(mintedToken.row.site_id)}
              {mintedToken.row.expected_serial ? <> · Serial lock: <span className="font-mono">{mintedToken.row.expected_serial}</span></> : null}
              {" "}· Expires: {formatRelative(mintedToken.row.expires_at)}
            </div>
          </CardBody>
        </Card>
      )}

      {showMint && (
        <Card className="mb-6">
          <CardHeader><CardTitle>Mint enrollment token</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onMintToken} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <Label>Site</Label>
                <select name="site_id" required
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {sites.map((s) => <option key={s.id} value={s.id}>{s.name} — {s.code}</option>)}
                </select>
              </div>
              <div>
                <Label>Serial (optional)</Label>
                <Input name="expected_serial" placeholder="APP-HQ-0001 (locks token to this serial)" />
              </div>
              <div>
                <Label>TTL (hours)</Label>
                <Input name="ttl_hours" type="number" defaultValue={24} min={1} max={168} />
              </div>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Minting…" : "Mint token"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New appliance</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-4 gap-3">
              <div>
                <Label>Site</Label>
                <select
                  name="site_id" required
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
                >
                  {sites.map((s) => <option key={s.id} value={s.id}>{s.name} — {s.code}</option>)}
                </select>
              </div>
              <div><Label>Serial</Label><Input name="serial" required placeholder="APP-HQ-0001" /></div>
              <div><Label>Name</Label><Input name="name" required placeholder="hq-gateway" /></div>
              <div><Label>Model</Label><Input name="model" placeholder="Protectli VP2410" /></div>
              <div className="sm:col-span-4 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card className="mb-6">
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No appliances yet" hint="Factory-clean appliances self-register under Onboarding; activate them there. Or mint a token above for a manual install." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Name</TH><TH>Site</TH><TH>Serial</TH>
                  <TH>Status</TH><TH>Version</TH><TH>Last seen</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((a) => (
                  <TR key={a.id}>
                    <TD>{a.name}<div className="text-xs text-muted font-mono">{a.id.slice(0, 8)}</div></TD>
                    <TD className="text-muted">{siteName(a.site_id)}</TD>
                    <TD className="font-mono">{a.serial}</TD>
                    <TD>
                      <LivePulse status={a.status} lastSeen={a.last_seen_at} />
                      <Badge tone={toneFor(a.status) as any}>{a.status}</Badge>
                    </TD>
                    <TD className="text-muted text-xs font-mono">{a.version || "—"}</TD>
                    <TD className="text-muted">{a.last_seen_at ? formatRelative(a.last_seen_at) : "—"}</TD>
                    <TD className="text-right whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => onShowEffective(a)}><Eye size={12} /> Config</Button>
                      <Button size="sm" variant="ghost" onClick={() => onDelete(a.id)}>Delete</Button>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {tokens && tokens.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>Enrollment tokens</CardTitle>
          </CardHeader>
          <CardBody className="p-0">
            <Table>
              <THead>
                <TR>
                  <TH>Hint</TH><TH>Site</TH><TH>Serial lock</TH>
                  <TH>Status</TH><TH>Expires</TH><TH>Created</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {tokens.map((t) => {
                  const consumed = !!t.consumed_at;
                  const expired = !consumed && new Date(t.expires_at) < new Date();
                  const tone = consumed ? "default" : expired ? "warn" : "info";
                  const label = consumed ? "consumed" : expired ? "expired" : "pending";
                  return (
                    <TR key={t.id}>
                      <TD className="font-mono">…{t.token_hint}</TD>
                      <TD className="text-muted">{siteName(t.site_id)}</TD>
                      <TD className="font-mono text-xs">{t.expected_serial || "—"}</TD>
                      <TD><Badge tone={tone as any}>{label}</Badge></TD>
                      <TD className="text-muted text-xs">{formatRelative(t.expires_at)}</TD>
                      <TD className="text-muted text-xs">{formatRelative(t.created_at)}</TD>
                      <TD className="text-right">
                        {!consumed && (
                          <Button size="sm" variant="ghost" onClick={() => onRevokeToken(t.id)}>
                            <Trash2 size={12} /> Revoke
                          </Button>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          </CardBody>
        </Card>
      )}

      {effOpen && (
        <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-6 z-50"
             onClick={() => setEffOpen(null)}>
          <div className="bg-panel border border-border rounded-md max-w-4xl w-full max-h-[85vh] overflow-auto"
               onClick={(e) => e.stopPropagation()}>
            <div className="p-4 border-b border-border flex items-center justify-between">
              <div>
                <div className="font-semibold">Effective config — {effOpen.name}</div>
                <div className="text-xs text-muted">
                  What scd at site <span className="font-mono">{siteName(effOpen.site_id)}</span> should be enforcing.
                </div>
              </div>
              <Button size="sm" variant="ghost" onClick={() => setEffOpen(null)}><X size={14} /></Button>
            </div>
            <div className="p-4 space-y-6">
              {!effData ? <EmptyState title="Loading…" /> : (
                <>
                  <div>
                    <div className="text-xs text-muted uppercase tracking-wider mb-2">
                      PMS providers ({effData.pms_providers?.length ?? 0})
                    </div>
                    {!effData.pms_providers || effData.pms_providers.length === 0 ? (
                      <div className="text-sm text-muted">No PMS providers configured.</div>
                    ) : (
                      <Table>
                        <THead><TR><TH>Name</TH><TH>Kind</TH><TH>Scope</TH><TH>Status</TH></TR></THead>
                        <tbody>
                          {effData.pms_providers.map((p) => (
                            <TR key={p.id}>
                              <TD>{p.display_name || p.name}</TD>
                              <TD className="text-muted">{p.kind}</TD>
                              <TD>{p.site_id
                                ? <Badge tone="info">site override</Badge>
                                : <Badge tone="default">tenant-wide</Badge>}</TD>
                              <TD><Badge tone={(p.status === "connected" ? "ok" : p.status === "down" ? "err" : "warn") as any}>{p.status}</Badge></TD>
                            </TR>
                          ))}
                        </tbody>
                      </Table>
                    )}
                  </div>
                  <div>
                    <div className="text-xs text-muted uppercase tracking-wider mb-2">
                      Walled-garden rules ({effData.walled_garden?.length ?? 0})
                    </div>
                    {!effData.walled_garden || effData.walled_garden.length === 0 ? (
                      <div className="text-sm text-muted">No walled-garden rules configured.</div>
                    ) : (
                      <Table>
                        <THead><TR><TH>Kind</TH><TH>Value</TH><TH>Ports</TH><TH>Scope</TH></TR></THead>
                        <tbody>
                          {effData.walled_garden.map((w) => (
                            <TR key={w.id}>
                              <TD className="text-muted">{w.kind}</TD>
                              <TD className="font-mono text-xs">{w.value}</TD>
                              <TD className="text-muted text-xs">{w.ports?.join(", ") || "any"}</TD>
                              <TD>{w.site_id
                                ? <Badge tone="info">site</Badge>
                                : <Badge tone="default">tenant-wide</Badge>}</TD>
                            </TR>
                          ))}
                        </tbody>
                      </Table>
                    )}
                  </div>
                </>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
