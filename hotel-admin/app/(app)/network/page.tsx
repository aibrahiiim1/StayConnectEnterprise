"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import {
  api, ListResp,
  GuestNetwork, GuestNetworkStatus, NetRevision,
  ValidateResult, ApplyResult, ValidationIssue, HealthCheck,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button, buttonVariants } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { PendingChangeBanner, ReadOnlyNotice, useSecondsLeft } from "@/components/ui/patterns";
import { CheckCircle2, Network, Pencil, Plus, Power, Trash2, Users, Wifi } from "lucide-react";
import { errMsg } from "@/lib/utils";
import {
  ApplyResults, DhcpModeBadge, networkTypeLabel, useNetworkAccess,
} from "@/components/network/shared";

function poolSummary(net: GuestNetwork): string {
  if (!net.pools || net.pools.length === 0) return "—";
  return net.pools.map((p) => `${p.start_ip}–${p.end_ip}`).join(", ");
}

export default function NetworkPage() {
  const [rows, setRows] = useState<GuestNetwork[] | null>(null);
  const [status, setStatus] = useState<Record<string, GuestNetworkStatus>>({});
  const [pending, setPending] = useState<NetRevision | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<null | "validate" | "apply" | "confirm" | "rollback">(null);
  const [acting, setActing] = useState<string | null>(null);
  const [validation, setValidation] = useState<{ ok: boolean; issues?: ValidationIssue[] } | null>(null);
  const [health, setHealth] = useState<HealthCheck[] | null>(null);
  const toast = useToast();

  const { known, writable } = useNetworkAccess();

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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // When the confirmation window runs out the appliance rolls back on its own; re-read once so the banner does
  // not sit at 0:00 offering buttons for a revision that no longer waits for anyone.
  const left = useSecondsLeft(pending?.confirm_deadline ?? null);
  const reloadedFor = useRef<string | null>(null);
  useEffect(() => {
    if (pending && left === 0 && reloadedFor.current !== pending.id) {
      reloadedFor.current = pending.id;
      const t = setTimeout(reload, 2000);
      return () => clearTimeout(t);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [left, pending]);

  // "Take offline" and "Delete" are STAGED changes: nothing happens to a guest until the configuration is applied.
  // The confirmation says so, because that is the difference between a scary button and a safe one.
  const [confirming, setConfirming] = useState<{ kind: "disable" | "delete"; net: GuestNetwork } | null>(null);
  const [confirmErr, setConfirmErr] = useState<string | null>(null);

  async function applyStagedRemoval() {
    if (!confirming) return;
    const { kind, net } = confirming;
    setActing(net.id); setConfirmErr(null);
    try {
      if (kind === "disable") await api.post(`/network/guest-networks/${net.id}/disable`);
      else await api.del(`/network/guest-networks/${net.id}`);
      setConfirming(null);
      toast.success(
        kind === "disable" ? `${net.name} is staged to go offline` : `${net.name} is staged for deletion`,
        "Nothing changes for guests until you apply the changes.",
      );
      reload();
    }
    catch (e) { setConfirmErr(errMsg(e)); }
    finally { setActing(null); }
  }

  async function onValidate() {
    setBusy("validate"); setErr(null); setHealth(null);
    try {
      const r = await api.post<ValidateResult>("/network/validate");
      setValidation(r.validation);
      if (r.validation.ok) toast.success("Validation passed", "The staged configuration can be applied.");
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(null); }
  }

  async function onApply() {
    setBusy("apply"); setErr(null);
    try {
      const r = await api.post<ApplyResult>("/network/apply", { summary: "apply from guest networks" });
      setValidation(r.validation ?? null);
      setHealth(r.health ?? null);
      if (r.state === "pending_confirmation") {
        toast.success("Applied — confirmation required", "Keep the change before the timer runs out, or it rolls back automatically.");
      } else if (r.state === "rolled_back") {
        setErr(r.message || "Apply rolled back after health checks failed.");
      } else if (r.state === "failed") {
        setErr(r.message || "Apply failed.");
      } else {
        toast.success(r.message || `Apply state: ${r.state}`);
      }
      reload();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(null); }
  }

  async function onConfirm(id: string) {
    setBusy("confirm"); setErr(null);
    try { await api.post(`/network/revisions/${id}/confirm`); toast.success("Configuration confirmed", "The applied change is now the active configuration."); reload(); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(null); }
  }

  async function onRollback(id: string) {
    setBusy("rollback"); setErr(null);
    try { await api.post(`/network/revisions/${id}/rollback`); toast.success("Configuration rolled back", "The previous configuration is back in place."); reload(); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(null); }
  }

  const enabledCount = (rows ?? []).filter((n) => n.enabled).length;
  const knownClients = Object.values(status);
  const clientTotal = knownClients.reduce((s, x) => s + (x.active_clients ?? 0), 0);
  const hasResults = !!validation || !!(health && health.length);

  const newButton = (
    <Link href="/network/new" className={buttonVariants({ variant: "primary" })}>
      <Plus /> New guest network
    </Link>
  );

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<Network />}
        eyebrow="Networking"
        title="Guest networks"
        description="The Wi-Fi networks guests join. Each one is a VLAN your wireless controller maps an SSID to, with its own addresses and sign-in page. Changes are staged, then applied with an automatic rollback."
        actions={writable && (
          <>
            <Button variant="secondary" disabled={busy !== null} onClick={onValidate}>
              <CheckCircle2 /> {busy === "validate" ? "Validating…" : "Validate"}
            </Button>
            <Button variant="secondary" disabled={busy !== null} onClick={onApply}>
              <Power /> {busy === "apply" ? "Applying…" : "Apply changes"}
            </Button>
            {newButton}
          </>
        )}
      />

      {known && !writable && <ReadOnlyNotice>Your role can view guest networks but not change them.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      {pending && (
        <PendingChangeBanner
          title={`Revision #${pending.seq} — confirm or it rolls back automatically`}
          description={
            <>
              The change is live now. Keep it to make it the active configuration; if nobody does
              {pending.confirm_deadline ? ` by ${new Date(pending.confirm_deadline).toLocaleTimeString()}` : " in time"},
              the appliance puts the previous configuration back on its own.
            </>
          }
          deadline={pending.confirm_deadline ?? null}
          canAct={writable}
          busy={busy === "confirm" ? "confirm" : busy === "rollback" ? "rollback" : null}
          onConfirm={() => onConfirm(pending.id)}
          onRollback={() => onRollback(pending.id)}
        >
          {hasResults ? <ApplyResults validation={validation} health={health} /> : undefined}
        </PendingChangeBanner>
      )}

      {!pending && hasResults && (
        <Card>
          <CardHeader>
            <div className="space-y-1">
              <CardTitle>Validation &amp; health</CardTitle>
              <CardDescription>The result of the last Validate or Apply on this screen.</CardDescription>
            </div>
          </CardHeader>
          <CardBody><ApplyResults validation={validation} health={health} /></CardBody>
        </Card>
      )}

      {rows !== null && rows.length > 0 && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <StatCard label="Guest networks" value={rows.length} icon={<Network />} />
          <StatCard
            label="Enabled"
            value={enabledCount}
            hint={rows.length - enabledCount > 0 ? `${rows.length - enabledCount} disabled` : "All enabled"}
            icon={<Wifi />}
            tone={enabledCount === 0 ? "warn" : "ok"}
          />
          <StatCard
            label="Devices connected"
            value={knownClients.length ? clientTotal : "—"}
            hint="Across all guest networks, now"
            icon={<Users />}
            tone="info"
          />
        </div>
      )}

      <Card>
        {rows === null ? (
          <SkeletonRows rows={4} cols={6} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Network />}
            title="No guest networks yet"
            hint="Create a guest network to give guests Wi-Fi with a sign-in page, addresses and internet access."
            action={writable ? newButton : undefined}
          />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Name</TH>
                <TH className="hidden lg:table-cell">SSID label</TH>
                <TH>Type</TH>
                <TH className="hidden xl:table-cell">Parent interface</TH>
                <TH className="hidden md:table-cell">Gateway</TH>
                <TH className="hidden lg:table-cell">Subnet</TH>
                <TH className="hidden md:table-cell">DHCP</TH>
                <TH className="hidden xl:table-cell">Pool</TH>
                <TH className="hidden sm:table-cell">Portal</TH>
                <TH>Status</TH>
                <TH className="text-end">Clients</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((n) => (
                <TR key={n.id}>
                  <TD className="min-w-40">
                    <Link href={`/network/${n.id}`} className="font-medium text-foreground hover:text-primary hover:underline">
                      {n.name}
                    </Link>
                    {n.description && <div className="text-caption text-muted-foreground">{n.description}</div>}
                  </TD>
                  <TD className="hidden text-muted-foreground lg:table-cell">{n.ssid_label || "—"}</TD>
                  <TD>
                    <Badge tone={n.network_type === "vlan" ? "info" : "default"}>{networkTypeLabel(n)}</Badge>
                  </TD>
                  <TD className="hidden font-mono text-xs xl:table-cell">{n.parent_interface}</TD>
                  <TD className="hidden font-mono text-xs md:table-cell">{n.gateway_ip}</TD>
                  <TD className="hidden font-mono text-xs lg:table-cell">{n.subnet_cidr}</TD>
                  <TD className="hidden md:table-cell"><DhcpModeBadge mode={n.dhcp_mode} /></TD>
                  <TD className="hidden font-mono text-xs text-muted-foreground xl:table-cell">{poolSummary(n)}</TD>
                  <TD className="hidden sm:table-cell">
                    {n.captive_portal_enabled ? <Badge tone="info">Sign-in page</Badge> : <Badge tone="default">Open</Badge>}
                  </TD>
                  <TD>{n.enabled ? <Badge tone="ok" dot>Enabled</Badge> : <Badge tone="default">Disabled</Badge>}</TD>
                  <TD className="text-end tabular text-muted-foreground">{status[n.id]?.active_clients ?? "—"}</TD>
                  <TD className="whitespace-nowrap text-end">
                    <div className="inline-flex items-center gap-1">
                      <Link
                        href={`/network/${n.id}`}
                        className={buttonVariants({ variant: "ghost", size: "sm" })}
                        aria-label={writable ? `Edit ${n.name}` : `View ${n.name}`}
                      >
                        <Pencil /> <span className="hidden sm:inline">{writable ? "Edit" : "View"}</span>
                      </Link>
                      {writable && n.enabled && (
                        <Button
                          size="sm" variant="ghost" disabled={acting === n.id}
                          aria-label={`Disable ${n.name}`}
                          onClick={() => { setConfirmErr(null); setConfirming({ kind: "disable", net: n }); }}
                        >
                          <Power /> <span className="hidden sm:inline">Disable</span>
                        </Button>
                      )}
                      {writable && !n.enabled && (
                        <Button
                          size="sm" variant="ghost" disabled={acting === n.id}
                          aria-label={`Delete ${n.name}`}
                          onClick={() => { setConfirmErr(null); setConfirming({ kind: "delete", net: n }); }}
                        >
                          <Trash2 /> <span className="hidden sm:inline">Delete</span>
                        </Button>
                      )}
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(v) => !v && setConfirming(null)}
        title={
          confirming?.kind === "delete"
            ? `Delete the network ${confirming.net.name}?`
            : `Take ${confirming?.net.name ?? ""} offline?`
        }
        description="This only stages the change. Nothing happens to guests until you apply the changes on this screen."
        consequences={
          confirming?.kind === "delete"
            ? [
                "The network and its address ranges are removed from the staged configuration.",
                "Once applied, any device on this network loses its connection and cannot reconnect.",
                "The SSID on your wireless controller is not changed; remove or remap it there.",
              ]
            : [
                "The network is marked disabled in the staged configuration.",
                "Guests on it stay connected until you apply the change; after that, nobody can join it.",
              ]
        }
        confirmLabel={confirming?.kind === "delete" ? "Delete network" : "Take offline"}
        confirmVariant="danger"
        busy={acting !== null && acting === confirming?.net.id}
        error={confirmErr}
        onConfirm={applyStagedRemoval}
      />
    </PageShell>
  );
}
