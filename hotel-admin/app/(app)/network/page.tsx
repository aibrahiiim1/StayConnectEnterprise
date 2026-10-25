"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  api, inBackground, ListResp,
  GuestNetwork, GuestNetworkStatus, NetRevision,
  ValidateResult, ApplyResult, ValidationIssue, HealthCheck,
} from "@/lib/api";
import { Card, SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button, LinkButton } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { SkeletonRows } from "@/components/ui/misc";
import { RowActions } from "@/components/ui/row-actions";
import { useToast } from "@/components/ui/toast";
import { PendingChangeBanner, ReadOnlyNotice, useSecondsLeft } from "@/components/ui/patterns";
import { CheckCircle2, Network, Plus, Power, Users, Wifi } from "lucide-react";
import { errMsg } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";
import {
  ApplyResults, DhcpModeBadge, EnabledBadge, networkTypeLabel, useNetworkAccess,
} from "@/components/network/shared";
import { isReauth, useNetworkChangeStepUp } from "@/components/network/change-step-up";
import { NetworkStatusBadge, PendingChangeNote } from "@/components/network/status";
import { FactoryNetworkCard, PortModeCard, StagedChangesList } from "@/components/network/lifecycle";
import { ArchivedNetworks, DefaultNetworkCard } from "@/components/network/default-network";
import type { NetworkOverview, OverviewNetwork } from "@/lib/api/network-overview";

function poolSummary(net: GuestNetwork): string {
  if (!net.pools || net.pools.length === 0) return "—";
  return net.pools.map((p) => `${p.start_ip}–${p.end_ip}`).join(", ");
}

export default function NetworkPage() {
  const [rows, setRows] = useState<GuestNetwork[] | null>(null);
  const [status, setStatus] = useState<Record<string, GuestNetworkStatus>>({});
  const [overview, setOverview] = useState<Record<string, OverviewNetwork>>({});
  const [blocked, setBlocked] = useState<string | null>(null);
  const [pending, setPending] = useState<NetRevision | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<null | "validate" | "apply" | "confirm" | "rollback">(null);
  const [acting, setActing] = useState<string | null>(null);
  const [validation, setValidation] = useState<{ ok: boolean; issues?: ValidationIssue[] } | null>(null);
  const [health, setHealth] = useState<HealthCheck[] | null>(null);
  const [stagedKey, setStagedKey] = useState(0);
  const toast = useToast();
  const router = useRouter();

  const { known, writable } = useNetworkAccess();

  async function loadNetworks() {
    try {
      const r = await api.get<ListResp<GuestNetwork>>("/network/guest-networks");
      const data = r.data ?? [];
      setRows(data);
      const entries = await Promise.all(
        data.map(async (n) => {
          try { return [n.id, await api.get<GuestNetworkStatus>(`/network/guest-networks/${n.id}/status`)] as const; }
          catch { return null; }
        })
      );
      const map: Record<string, GuestNetworkStatus> = {};
      for (const e of entries) if (e) map[e[0]] = e[1];
      setStatus(map);
      // What each network IS (confirmed, running, observed) rather than what was saved. Older appliances answer 404 and keep the saved-state badge.
      try {
        const o = await api.get<NetworkOverview>("/network/overview");
        const byId: Record<string, OverviewNetwork> = {};
        for (const n of o.networks ?? []) byId[n.id] = n;
        setOverview(byId);
        setBlocked(o.changes_blocked ? (o.blocked_detail ?? "Changes are being refused.") : null);
      } catch { setOverview({}); setBlocked(null); }
    } catch (e) { setErr(errMsg(e)); }
  }

  async function loadRevisions() {
    try {
      const r = await api.get<ListResp<NetRevision>>("/network/revisions");
      const newest = (r.data ?? [])[0] ?? null;
      setPending(newest && newest.state === "pending_confirmation" ? newest : null);
    } catch { /* revisions optional on the landing page */ }
  }

  function reload() { loadNetworks(); loadRevisions(); setStagedKey((k) => k + 1); }

  useEffect(() => {
    reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Refresh after the deadline because the appliance rolls back automatically.
  const left = useSecondsLeft(pending?.confirm_deadline ?? null);
  const reloadedFor = useRef<string | null>(null);
  useEffect(() => {
    if (pending && left === 0 && reloadedFor.current !== pending.id) {
      reloadedFor.current = pending.id;
      const t = setTimeout(() => void inBackground(reload), 2000);
      return () => clearTimeout(t);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [left, pending]);

  // Taking a network offline or deleting it is staged until configuration is applied.
  const [confirming, setConfirming] = useState<{ kind: "disable" | "delete"; net: GuestNetwork } | null>(null);
  const [confirmErr, setConfirmErr] = useState<string | null>(null);

  async function enableNetwork(net: GuestNetwork) {
    setActing(net.id);
    try {
      await api.post(`/network/guest-networks/${net.id}/enable`);
      toast.success(`${net.name} is staged to come back online`, "Apply the changes on this screen to bring it online.");
      reload();
    } catch (e) { toast.error("Could not enable the network", errMsg(e)); }
    finally { setActing(null); }
  }

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
        "Nothing changes for clients until you apply the changes.",
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

  const stepUp = useNetworkChangeStepUp();

  function onApply() {
    stepUp.ask("apply", (password) => doApply(password));
  }

  async function doApply(password: string) {
    setBusy("apply"); setErr(null);
    try {
      const r = await api.post<ApplyResult>("/network/apply", { summary: "apply from client networks", password });
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
    } catch (e) { if (isReauth(e)) throw e; setErr(errMsg(e)); }
    finally { setBusy(null); }
  }

  function onConfirm(id: string) {
    stepUp.ask("confirm", async (password) => {
      setBusy("confirm"); setErr(null);
      try { await api.post(`/network/revisions/${id}/confirm`, { password }); toast.success("Configuration confirmed", "The applied change is now the active configuration."); reload(); }
      catch (e) { if (isReauth(e)) throw e; setErr(errMsg(e)); }
      finally { setBusy(null); }
    });
  }

  function onRollback(id: string) {
    stepUp.ask("rollback", async (password) => {
      setBusy("rollback"); setErr(null);
      try { await api.post(`/network/revisions/${id}/rollback`, { password }); toast.success("Configuration rolled back", "The previous configuration is back in place."); reload(); }
      catch (e) { if (isReauth(e)) throw e; setErr(errMsg(e)); }
      finally { setBusy(null); }
    });
  }

  const enabledCount = (rows ?? []).filter((n) => n.enabled).length;
  const shownRows = (rows ?? []).filter((n) => !n.archived);
  const archivedCount = (rows ?? []).length - shownRows.length;
  const knownClients = Object.values(status);
  const clientTotal = knownClients.reduce((s, x) => s + (x.active_clients ?? 0), 0);
  const hasResults = !!validation || !!(health && health.length);

  const newButton = (
    <LinkButton href="/network/new" variant="primary">
      <Plus /> New client network
    </LinkButton>
  );

  return (
    <PageShell width="wide">
      <PageHeader
        title="Client networks"
        description="The Wi-Fi networks clients join."
        help={
          <>
            <HelpSection title="What a client network is">
              <p>
                Each client network is a VLAN your wireless controller maps an SSID to, with its own addresses and
                sign-in page.
              </p>
            </HelpSection>
            <HelpSection title="How changes reach clients">
              <HelpList
                items={[
                  <>Creating, editing, disabling or deleting a network only <strong>stages</strong> the change.</>,
                  <><strong>Validate</strong> checks the staged configuration without touching anything live.</>,
                  <><strong>Apply changes</strong> makes it live and starts a confirmation timer.</>,
                  <>If nobody keeps the change before the timer runs out, the appliance rolls back on its own.</>,
                ]}
              />
            </HelpSection>
          </>
        }
        actions={writable && (
          <>
            <Button variant="secondary" disabled={busy !== null} onClick={onValidate}>
              <CheckCircle2 /> {busy === "validate" ? "Validating…" : "Validate"}
            </Button>
            <Button variant="secondary" disabled={busy !== null} onClick={onApply}>
              <Power /> {busy === "apply" ? "Applying…" : "Apply changes"}
            </Button>
            <LinkButton variant="secondary" href="/network/recovery">Recovery</LinkButton>
            {newButton}
          </>
        )}
      />

      {known && !writable && <ReadOnlyNotice>Your role can view client networks but not change them.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      {blocked && (
        <Callout tone="warning" title="Changes are being refused">
          {blocked} The appliance re-checks by itself; once it is back on its confirmed configuration this clears.
        </Callout>
      )}

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
        <SectionCard title="Validation & health" description="The result of the last Validate or Apply on this screen.">
          <ApplyResults validation={validation} health={health} />
        </SectionCard>
      )}

      <FactoryNetworkCard key={`f${stagedKey}`} writable={writable} onStaged={reload} />
      <StagedChangesList writable={writable} reloadKey={stagedKey} onCancelled={reload} />
      <DefaultNetworkCard writable={writable} reloadKey={stagedKey} />

      {rows !== null && rows.length > 0 && (
        <StatGrid cols={3}>
          <StatCard label="Client networks" value={shownRows.length} hint={archivedCount > 0 ? `${archivedCount} archived` : undefined} icon={<Network />} />
          <StatCard
            label="Enabled"
            value={enabledCount}
            hint={shownRows.length - enabledCount > 0 ? `${shownRows.length - enabledCount} disabled` : "All enabled"}
            icon={<Wifi />}
            tone={enabledCount === 0 ? "warn" : "default"}
          />
          <StatCard
            label="Devices connected"
            value={knownClients.length ? clientTotal : "—"}
            hint="On all client networks, now"
            icon={<Users />}
          />
        </StatGrid>
      )}

      <Card className="overflow-hidden">
        {rows === null ? (
          <SkeletonRows rows={4} cols={6} />
        ) : shownRows.length === 0 ? (
          <EmptyState
            icon={<Network />}
            title="No client networks yet"
            hint="Create one to give clients Wi-Fi with a sign-in page, addresses and internet access."
            action={writable ? newButton : undefined}
          />
        ) : (
          <Table label="Client networks">
            <THead>
              <TR>
                <TH>Name</TH>
                <TH>Type</TH>
                <TH className="hidden 2xl:table-cell">Parent interface</TH>
                <TH className="hidden md:table-cell">Gateway / subnet</TH>
                <TH className="hidden 2xl:table-cell">DHCP</TH>
                <TH className="hidden 2xl:table-cell">Pool</TH>
                <TH className="hidden sm:table-cell">Portal</TH>
                <TH>Status</TH>
                <TH numeric>Clients</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {shownRows.map((n) => (
                <TR key={n.id} onClick={() => router.push(`/network/${n.id}`)}>
                  <TD className="min-w-44 max-w-[13rem]">
                    <Link
                      href={`/network/${n.id}`}
                      onClick={(e) => e.stopPropagation()}
                      title={n.name}
                      className="block truncate font-medium text-foreground hover:text-primary hover:underline"
                    >
                      {n.name}
                    </Link>
                    {n.is_default && <Badge tone="accent">Default</Badge>}
                    {(n.ssid_label || n.description) && (
                      <div className="truncate text-xs text-muted-foreground" title={n.description || n.ssid_label}>
                        {n.ssid_label ? `SSID ${n.ssid_label}` : n.description}
                      </div>
                    )}
                  </TD>
                  <TD nowrap>
                    <Badge tone={n.network_type === "vlan" ? "info" : "default"}>{networkTypeLabel(n)}</Badge>
                  </TD>
                  <TD nowrap className="hidden font-mono text-xs 2xl:table-cell">{n.parent_interface}</TD>
                  <TD nowrap className="hidden font-mono text-xs md:table-cell">
                    <div>{n.gateway_ip}</div>
                    <div className="text-muted-foreground">{n.subnet_cidr}</div>
                  </TD>
                  <TD nowrap className="hidden 2xl:table-cell"><DhcpModeBadge mode={n.dhcp_mode} /></TD>
                  <TD nowrap muted className="hidden font-mono text-xs 2xl:table-cell">{poolSummary(n)}</TD>
                  <TD nowrap className="hidden sm:table-cell">
                    {n.captive_portal_enabled ? <Badge tone="info">Sign-in page</Badge> : (
                      <>
                        <Badge tone="warn">No sign-in page</Badge>
                        <div className="mt-1 text-caption text-warning-subtle-foreground">No internet</div>
                      </>
                    )}
                  </TD>
                  <TD nowrap>
                    {overview[n.id] ? (
                      <>
                        <NetworkStatusBadge status={overview[n.id].status} reason={overview[n.id].reason} />
                        <PendingChangeNote changes={overview[n.id].changes} />
                      </>
                    ) : (
                      <EnabledBadge enabled={n.enabled} />
                    )}
                  </TD>
                  <TD numeric muted>{overview[n.id]?.active_clients ?? status[n.id]?.active_clients ?? "—"}</TD>
                  <TD className="w-0">
                    <RowActions
                      subject={n.name}
                      primary={{ label: writable ? "Edit" : "View", href: `/network/${n.id}` }}
                      items={[
                        {
                          label: "Disable",
                          hidden: !writable || !n.enabled,
                          disabled: acting === n.id,
                          onSelect: () => { setConfirmErr(null); setConfirming({ kind: "disable", net: n }); },
                        },
                        {
                          label: "Enable",
                          hidden: !writable || n.enabled,
                          disabled: acting === n.id,
                          onSelect: () => void enableNetwork(n),
                        },
                        {
                          label: "Delete",
                          danger: true,
                          hidden: !writable || n.enabled,
                          disabled: acting === n.id,
                          onSelect: () => { setConfirmErr(null); setConfirming({ kind: "delete", net: n }); },
                        },
                      ]}
                    />
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <ArchivedNetworks writable={writable} reloadKey={stagedKey} onRestored={reload} />

      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(v) => !v && setConfirming(null)}
        title={
          confirming?.kind === "delete"
            ? `Delete the network ${confirming.net.name}?`
            : `Take ${confirming?.net.name ?? ""} offline?`
        }
        description="This only stages the change. Nothing happens to clients until you apply the changes on this screen."
        consequences={
          confirming?.kind === "delete"
            ? [
                "The network and its address ranges are removed from the staged configuration.",
                "Once applied, any device on this network loses its connection and cannot reconnect.",
                "The SSID on your wireless controller is not changed; remove or remap it there.",
              ]
            : [
                "The network is marked disabled in the staged configuration.",
                "Clients on it stay connected until you apply the change; after that, nobody can join it.",
              ]
        }
        confirmLabel={confirming?.kind === "delete" ? "Delete network" : "Take offline"}
        confirmVariant="danger"
        busy={acting !== null && acting === confirming?.net.id}
        error={confirmErr}
        onConfirm={applyStagedRemoval}
      />
      {stepUp.dialog}
      <PortModeCard writable={writable} reloadKey={stagedKey} onStaged={reload} />
    </PageShell>
  );
}
