"use client";

import { useEffect, useState } from "react";
import { api, Whoami, CloudStatus } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Callout } from "@/components/ui/error-banner";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";
import { describeOutbox } from "@/lib/health-words";
import { Cloud, RefreshCw, Activity, Download } from "lucide-react";

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border py-1 text-sm">
      <span className="text-muted-foreground">{k}</span>
      <span className="text-right">{v ?? "—"}</span>
    </div>
  );
}

function maskUrl(u?: string): string {
  if (!u) return "—";
  // Strip any embedded credentials (scheme://user:pass@host -> scheme://host)
  return u.replace(/(\w+:\/\/)[^@/]*@/, "$1");
}

function ConnBadge({ state }: { state?: string }) {
  const map: Record<string, string> = {
    connected: "bg-success-subtle text-success-subtle-foreground",
    offline_cached: "bg-warning-subtle text-warning-subtle-foreground",
    disconnected: "bg-destructive-subtle text-destructive-subtle-foreground",
  };
  const label: Record<string, string> = {
    connected: "Connected",
    offline_cached: "Cloud unreachable — running on cached license",
    disconnected: "Disconnected",
  };
  return <Badge className={map[state ?? "disconnected"]}>{label[state ?? "disconnected"] ?? state}</Badge>;
}

export default function CloudConnectionPage() {
  const [roles, setRoles] = useState<string[]>([]);
  const [st, setSt] = useState<CloudStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const writable = canWrite("network", roles);

  async function load() {
    try { setSt(await api.get<CloudStatus>("/network/cloud")); }
    catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function testConn() { setBusy(true); setErr(null); try { setSt(await api.post<CloudStatus>("/network/cloud/test", {})); } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); } }
  async function refreshLicense() { setBusy(true); setErr(null); try { await api.post("/network/cloud/refresh-license", {}); await load(); } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); } }

  function downloadDiag() {
    if (!st) return;
    const blob = new Blob([JSON.stringify(st, null, 2)], { type: "application/json" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `cloud-connection-${new Date().toISOString().slice(0, 19)}.json`;
    a.click();
  }

  if (!st) return <div className="p-6 text-sm text-muted-foreground">{err ?? "Loading cloud connection…"}</div>;
  const c = st.cloud, l = st.license, o = st.outbox, conn = st.connection;
  // Described in one place (lib/health-words) so this page, the dashboard and the header pill cannot end up
  // telling an operator three different things about the same queue.
  const outbox = describeOutbox(o ? { enabled: !!o.enabled, pending: o.pending, dead: o.dead, oldest_pending: o.oldest_pending } : null);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight sm:text-2xl"><Cloud className="h-5 w-5" />Cloud connection</h1>
          <p className="text-sm text-muted-foreground">Live status of the link between this appliance and the Central Control Plane. The appliance operates locally even when the cloud is unreachable.</p>
        </div>
        <div className="flex items-center gap-2"><ConnBadge state={conn.state} />
          <Button variant="secondary" onClick={load}><RefreshCw className="mr-1 h-4 w-4" />Refresh</Button>
        </div>
      </div>

      {err && <div className="rounded-md border border-destructive/25 bg-destructive-subtle p-3 text-sm text-destructive-subtle-foreground">{err}</div>}

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader><CardTitle>Cloud API (mTLS)</CardTitle></CardHeader>
          <CardBody>
            <Row k="Status" v={c.api_mtls?.mtls_ready
              ? <Badge className="bg-success-subtle text-success-subtle-foreground">Active</Badge>
              : <Badge className="bg-warning-subtle text-warning-subtle-foreground">Not ready</Badge>} />
            <Row k="Cert fingerprint" v={c.api_mtls?.cert_fingerprint
              ? <code>{c.api_mtls.cert_fingerprint.slice(0, 16)}…</code> : "—"} />
            <Row k="Cert expires" v={c.api_mtls?.not_after ?? "—"} />
          </CardBody>
        </Card>

        <Card>
          <CardHeader><CardTitle>NATS (mTLS)</CardTitle></CardHeader>
          <CardBody>
            <Row k="Status" v={c.nats_mtls?.connected
              ? <Badge className="bg-success-subtle text-success-subtle-foreground">Connected</Badge>
              : <Badge className="bg-warning-subtle text-warning-subtle-foreground">Disconnected</Badge>} />
            <Row k="Mode" v={c.nats_mtls?.mtls
              ? <Badge className="bg-success-subtle text-success-subtle-foreground">mTLS</Badge>
              : <Badge className="bg-warning-subtle text-warning-subtle-foreground">legacy</Badge>} />
            <Row k="URL" v={<code>{maskUrl(c.nats_mtls?.url)}</code>} />
          </CardBody>
        </Card>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader><CardTitle>Central Control Plane</CardTitle></CardHeader>
          <CardBody>
            <Row k="Cloud API URL" v={<code>{c.cloud_api_url}</code>} />
            <Row k="Transport (NATS)" v={<code>{c.nats_url}</code>} />
            <Row k="Reachable now" v={conn.reachable ? <span className="text-success-subtle-foreground">yes (HTTP {conn.http_code})</span> : <span className="text-destructive">no{conn.error ? ` — ${conn.error}` : ""}</span>} />
            <Row k="Certificate valid" v={conn.cert_valid ? <span className="text-success-subtle-foreground">yes (verified via CA)</span> : <span className="text-warning-subtle-foreground">not verified</span>} />
            <Row k="Enrollment" v={c.enrolled ? <Badge className="bg-success-subtle text-success-subtle-foreground">enrolled</Badge> : <Badge className="bg-destructive-subtle text-destructive-subtle-foreground">not enrolled</Badge>} />
          </CardBody>
        </Card>

        <Card>
          <CardHeader><CardTitle>Appliance identity</CardTitle></CardHeader>
          <CardBody>
            <Row k="Appliance ID" v={<code>{c.appliance_id}</code>} />
            <Row k="Serial" v={<code>{c.serial || "—"}</code>} />
            <Row k="Site ID" v={<code>{c.site_id}</code>} />
            <Row k="Customer ID" v={<code>{c.tenant_id}</code>} />
          </CardBody>
        </Card>

        <Card>
          <CardHeader><CardTitle>License</CardTitle></CardHeader>
          <CardBody>
            <Row k="State" v={<Badge className={l.state === "Active" ? "bg-success-subtle text-success-subtle-foreground" : "bg-warning-subtle text-warning-subtle-foreground"}>{l.state}</Badge>} />
            <Row k="Valid until" v={l.valid_until} />
            <Row k="Offline grace" v={l.offline_grace_days != null ? `${l.offline_grace_days} days` : "—"} />
            <Row k="Cloud validation stale" v={l.cloud_stale ? "yes" : "no"} />
            <Row k="Last cloud validation" v={l.last_cloud_validation ?? "—"} />
          </CardBody>
        </Card>

        {/*
          THE OUTBOX, WITH ITS MEANING ATTACHED.
          ------------------------------------------------------------------------------------------------
          This card was headed "Telemetry outbox" and listed "Pending", "Dead-letter" and "Oldest pending" as
          four bare values. "Dead-letter" is message-queue vocabulary, not hotel vocabulary, and nothing on the
          page said whether a large figure meant guests were affected — which is the only question an operator
          actually has about it. The numbers are unchanged; the sentence that makes them usable is new, and it
          is shared with the dashboard and the health pill so all three say the same thing.
        */}
        <Card>
          <CardHeader><CardTitle>Reporting to the StayConnect cloud</CardTitle></CardHeader>
          <CardBody className="space-y-3">
            <Row k="In use" v={o.enabled ? "yes" : "no"} />
            <Row
              k="Waiting to be sent"
              v={<b className={((o.pending ?? 0) > 0) ? "text-warning-subtle-foreground" : ""}>
                {(o.pending ?? 0).toLocaleString()}
              </b>}
            />
            <Row
              k="Given up on"
              v={<b className={((o.dead ?? 0) > 0) ? "text-destructive" : ""}>
                {(o.dead ?? 0).toLocaleString()}
              </b>}
            />
            <Row k="Oldest still waiting" v={o.oldest_pending ?? "—"} />
            <Callout
              tone={outbox.tone === "ok" ? "success" : outbox.tone === "err" ? "warning" : outbox.tone === "warn" ? "warning" : "neutral"}
              title={outbox.headline}
            >
              {outbox.summary}
            </Callout>
          </CardBody>
        </Card>
      </div>

      {writable && (
        <div className="flex gap-2">
          <Button onClick={testConn} disabled={busy}><Activity className="mr-1 h-4 w-4" />Test connection</Button>
          <Button variant="secondary" onClick={refreshLicense} disabled={busy}>Refresh license</Button>
          <Button variant="secondary" onClick={downloadDiag}><Download className="mr-1 h-4 w-4" />Download diagnostics</Button>
        </div>
      )}
      <p className="text-xs text-muted-foreground">Connection state is derived from a live probe and real outbox/heartbeat facts — never a hardcoded status. Secrets (NATS password, keys, tokens) are never displayed.</p>
    </div>
  );
}
