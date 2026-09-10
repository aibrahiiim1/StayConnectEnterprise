"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import {
  api, ListResp, Whoami,
  GuestNetwork, GuestNetworkStatus, NetRevision,
  ValidateResult, ApplyResult, ValidationIssue, HealthCheck,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Plus } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";

function dhcpTone(mode: string): "ok" | "warn" | "err" | "info" | "default" {
  switch (mode) {
    case "local":    return "ok";
    case "relay":    return "info";
    case "external": return "warn";
    case "disabled": return "err";
    default:         return "default";
  }
}

function poolSummary(net: GuestNetwork): string {
  if (!net.pools || net.pools.length === 0) return "—";
  return net.pools.map((p) => `${p.start_ip}–${p.end_ip}`).join(", ");
}

export default function NetworkPage() {
  const [rows, setRows] = useState<GuestNetwork[] | null>(null);
  const [status, setStatus] = useState<Record<string, GuestNetworkStatus>>({});
  const [roles, setRoles] = useState<string[]>([]);
  const [pending, setPending] = useState<NetRevision | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [acting, setActing] = useState<string | null>(null);
  const [validation, setValidation] = useState<{ ok: boolean; issues?: ValidationIssue[] } | null>(null);
  const [health, setHealth] = useState<HealthCheck[] | null>(null);

  const writable = canWrite("network", roles);

  async function loadNetworks() {
    try {
      const r = await api.get<ListResp<GuestNetwork>>("/network/guest-networks");
      const data = r.data ?? [];
      setRows(data);
      // fetch per-network status (active clients) — best-effort.
      const entries = await Promise.all(
        data.map(async (n) => {
          try { return [n.id, await api.get<GuestNetworkStatus>(`/network/guest-networks/${n.id}/status`)] as const; }
          catch { return null; }
        })
      );
      const map: Record<string, GuestNetworkStatus> = {};
      for (const e of entries) if (e) map[e[0]] = e[1];
      setStatus(map);
    } catch (e) { setErr(errMsg(e)); }
  }

  async function loadRevisions() {
    try {
      const r = await api.get<ListResp<NetRevision>>("/network/revisions");
      const newest = (r.data ?? [])[0] ?? null;
      setPending(newest && newest.state === "pending_confirmation" ? newest : null);
    } catch { /* revisions optional on the landing page */ }
  }

  function reload() { loadNetworks(); loadRevisions(); }

  useEffect(() => {
    reload();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);

  // THESE TWO TOOK A window.confirm, for actions that take a property's Wi-Fi offline.
  //
  // "Delete this guest network permanently?" was the entire explanation offered before removing the network every
  // guest on a VLAN is connected through. A browser confirm also cannot say the thing that matters most here: in
  // this product a network change is STAGED, and nothing happens to a guest until it is applied — which is the
  // difference between a scary button and a safe one, and it was nowhere on screen.
  const [confirming, setConfirming] = useState<{ kind: "disable" | "delete"; net: GuestNetwork } | null>(null);

  async function applyStagedRemoval() {
    if (!confirming) return;
    const { kind, net } = confirming;
    setActing(net.id); setErr(null);
    try {
      if (kind === "disable") await api.post(`/network/guest-networks/${net.id}/disable`);
      else await api.del(`/network/guest-networks/${net.id}`);
      setConfirming(null);
      reload();
    }
    catch (e) { setErr(errMsg(e)); }
    finally { setActing(null); }
  }

  async function onValidate() {
    setBusy(true); setErr(null); setMsg(null); setHealth(null);
    try {
      const r = await api.post<ValidateResult>("/network/validate");
      setValidation(r.validation);
      setMsg(r.validation.ok ? "Validation passed." : null);
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onApply() {
    setBusy(true); setErr(null); setMsg(null);
    try {
      const r = await api.post<ApplyResult>("/network/apply", { summary: "apply from guest networks" });
      setValidation(r.validation ?? null);
      setHealth(r.health ?? null);
      if (r.state === "pending_confirmation") {
        setMsg("Applied — confirm within 120s or it rolls back automatically.");
      } else if (r.state === "rolled_back") {
        setErr(r.message || "Apply rolled back after health checks failed.");
      } else if (r.state === "failed") {
        setErr(r.message || "Apply failed.");
      } else {
        setMsg(r.message || `Apply state: ${r.state}`);
      }
      reload();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onConfirm(id: string) {
    setBusy(true); setErr(null);
    try { await api.post(`/network/revisions/${id}/confirm`); setMsg("Configuration confirmed."); reload(); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onRollback(id: string) {
    setBusy(true); setErr(null);
    try { await api.post(`/network/revisions/${id}/rollback`); setMsg("Configuration rolled back."); reload(); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  return (
    <div className="mx-auto w-full max-w-7xl space-y-5">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">Networking</div>
          <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">Guest networks</h1>
        </div>
        {writable && (
          <div className="flex gap-2">
            <Button variant="secondary" disabled={busy} onClick={onValidate}>Validate</Button>
            <Button variant="secondary" disabled={busy} onClick={onApply}>Apply changes</Button>
            <Link
              href="/network/new"
              className="inline-flex h-9 items-center gap-2 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground hover:bg-primary-hover"
            >
              <Plus size={14} /> New guest network
            </Link>
          </div>
        )}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {msg && <div className="text-ok text-sm mb-4">{msg}</div>}

      {pending && (
        <div className="mb-6 rounded-md border border-warning/30 bg-warning-subtle px-4 py-3 text-sm text-warning-subtle-foreground flex items-center justify-between gap-4">
          <div>
            <div className="font-medium">Configuration pending confirmation</div>
            <div className="text-xs mt-0.5">
              Revision #{pending.seq} — confirm or it rolls back automatically
              {pending.confirm_deadline ? ` (deadline ${new Date(pending.confirm_deadline).toLocaleTimeString()})` : ""}.
            </div>
          </div>
          {writable && (
            <div className="flex gap-2 shrink-0">
              <Button size="sm" disabled={busy} onClick={() => onConfirm(pending.id)}>Confirm</Button>
              <Button size="sm" variant="secondary" disabled={busy} onClick={() => onRollback(pending.id)}>Rollback</Button>
            </div>
          )}
        </div>
      )}

      {(validation || health) && (
        <Card className="mb-6">
          <CardHeader><CardTitle>Validation &amp; health</CardTitle></CardHeader>
          <CardBody className="space-y-3 text-sm">
            {validation && (
              <div>
                <div className="flex items-center gap-2 mb-1">
                  <span className="text-muted-foreground">Validation</span>
                  <Badge tone={validation.ok ? "ok" : "err"}>{validation.ok ? "ok" : "issues"}</Badge>
                </div>
                {validation.issues && validation.issues.length > 0 && (
                  <ul className="space-y-1">
                    {validation.issues.map((i, k) => (
                      <li key={k} className="text-err text-xs">
                        <span className="font-mono">{i.field}</span> — {i.message} <span className="text-muted-foreground">({i.code})</span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            )}
            {health && health.length > 0 && (
              <div>
                <div className="text-muted mb-1">Health checks</div>
                <ul className="space-y-1">
                  {health.map((h, k) => (
                    <li key={k} className="flex items-center gap-2 text-xs">
                      <Badge tone={h.ok ? "ok" : "err"}>{h.ok ? "ok" : "fail"}</Badge>
                      <span className="font-mono">{h.name}</span>
                      {h.detail && <span className="text-muted-foreground">{h.detail}</span>}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No guest networks" hint="Create a guest network to broadcast WiFi with captive portal, DHCP and NAT." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Name</TH><TH>SSID label</TH><TH>Type</TH><TH>Parent</TH>
                  <TH>Gateway</TH><TH>Subnet</TH><TH>DHCP</TH><TH>Pool</TH>
                  <TH>Portal</TH><TH>Enabled</TH><TH>Clients</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((n) => (
                  <TR key={n.id}>
                    <TD>
                      <Link href={`/network/${n.id}`} className="hover:text-brand">{n.name}</Link>
                      {n.description && <div className="text-xs text-muted-foreground">{n.description}</div>}
                    </TD>
                    <TD className="text-muted-foreground">{n.ssid_label || "—"}</TD>
                    <TD>
                      {n.network_type === "vlan"
                        ? <Badge tone="info">VLAN {n.vlan_id ?? "?"}</Badge>
                        : <Badge tone="default">untagged</Badge>}
                    </TD>
                    <TD className="font-mono text-xs">{n.parent_interface}</TD>
                    <TD className="font-mono text-xs">{n.gateway_ip}</TD>
                    <TD className="font-mono text-xs">{n.subnet_cidr}</TD>
                    <TD><Badge tone={dhcpTone(n.dhcp_mode)}>{n.dhcp_mode}</Badge></TD>
                    <TD className="font-mono text-xs text-muted">{poolSummary(n)}</TD>
                    <TD>{n.captive_portal_enabled ? <Badge tone="info">portal</Badge> : <Badge tone="default">open</Badge>}</TD>
                    <TD>{n.enabled ? <Badge tone="ok">on</Badge> : <Badge tone="default">off</Badge>}</TD>
                    <TD className="text-muted-foreground">{status[n.id]?.active_clients ?? "—"}</TD>
                    <TD className="text-right space-x-2 whitespace-nowrap">
                      <Link href={`/network/${n.id}`} className="text-sm text-muted hover:text-text">Edit</Link>
                      {writable && n.enabled && (
                        <Button size="sm" variant="ghost" disabled={acting === n.id} onClick={() => setConfirming({ kind: "disable", net: n })}>Disable</Button>
                      )}
                      {writable && !n.enabled && (
                        <Button size="sm" variant="ghost" disabled={acting === n.id} onClick={() => setConfirming({ kind: "delete", net: n })}>Delete</Button>
                      )}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(v) => !v && setConfirming(null)}
        title={
          confirming?.kind === "delete"
            ? `Delete the network "${confirming.net.name}"?`
            : `Take "${confirming?.net.name}" offline?`
        }
        description={
          confirming?.kind === "delete"
            ? "The network and its address ranges are removed from the staged configuration. Nothing happens to guests until you apply the change — and once applied, any device on this network loses its connection and cannot reconnect."
            : "The network is marked disabled in the staged configuration. Guests on it stay connected until you apply the change; after that, nobody can join it."
        }
        confirmLabel={confirming?.kind === "delete" ? "Delete network" : "Disable network"}
        confirmVariant="danger"
        busy={acting === confirming?.net.id}
        onConfirm={applyStagedRemoval}
      />
    </div>
  );
}
