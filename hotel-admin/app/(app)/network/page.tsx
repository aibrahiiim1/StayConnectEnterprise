"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  api, ApiError, inBackground, ListResp,
  GuestNetwork, GuestNetworkStatus, NetRevision,
  ValidateResult, ApplyResult, ValidationIssue, HealthCheck,
} from "@/lib/api";
import { Card, SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button, LinkButton } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { SkeletonRows } from "@/components/ui/misc";
import { RowActions } from "@/components/ui/row-actions";
import { useToast } from "@/components/ui/toast";
import { LiveStatus, PendingChangeBanner, ReadOnlyNotice, useSecondsLeft } from "@/components/ui/patterns";
import { usePoll } from "@/lib/use-poll";
import { AlertTriangle, CheckCircle2, Network, Plus, Power, RefreshCw, Users, Wifi } from "lucide-react";
import { cn } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";
import {
  ApplyResults, DhcpModeBadge, EnabledBadge, netErrorWords, networkTypeLabel, plural, revisionStateLabel, useNetworkAccess,
} from "@/components/network/shared";
import { isReauth, useNetworkChangeStepUp } from "@/components/network/change-step-up";
import { NetworkStatusBadge, PendingChangeNote } from "@/components/network/status";
import { FactoryNetworkCard, PortModeCard, StagedChangesList, type LoadReport } from "@/components/network/lifecycle";
import { ArchivedNetworks, DefaultNetworkCard } from "@/components/network/default-network";
import { topologyWords } from "@/lib/network-state";
import { POLICY_WORDS, STATE_WORDS, type RoomSignInBrief } from "@/lib/api/pms-routing";
import type { NetworkOverview, OverviewNetwork } from "@/lib/api/network-overview";
import { servingCount } from "@/lib/api/network-overview";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const PARTS = ["Factory network", "Staged changes", "Default Network", "Client ports", "Archived networks"] as const;
type Part = (typeof PARTS)[number];

function poolSummary(net: GuestNetwork): string {
  if (!net.pools || net.pools.length === 0) return "Not set";
  return net.pools.map((p) => `${p.start_ip}–${p.end_ip}`).join(", ");
}

function listWords(items: string[]): string {
  if (items.length <= 1) return items.join("");
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

export default function NetworkPage() {
  const [rows, setRows] = useState<GuestNetwork[] | null>(null);
  const [status, setStatus] = useState<Record<string, GuestNetworkStatus>>({});
  const [overview, setOverview] = useState<Record<string, OverviewNetwork>>({});
  const [overviewFailed, setOverviewFailed] = useState(false);
  const [blocked, setBlocked] = useState<string | null>(null);
  const [pending, setPending] = useState<NetRevision | null>(null);
  const [revisionsFailed, setRevisionsFailed] = useState(false);
  const [partsFailed, setPartsFailed] = useState<Partial<Record<Part, boolean>>>({});
  const [err, setErr] = useState<string | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [busy, setBusy] = useState<null | "validate" | "apply" | "confirm" | "rollback">(null);
  const [acting, setActing] = useState<string | null>(null);
  const [validation, setValidation] = useState<{ ok: boolean; issues?: ValidationIssue[] } | null>(null);
  const [health, setHealth] = useState<HealthCheck[] | null>(null);
  const [stagedKey, setStagedKey] = useState(0);
  const toast = useToast();
  const router = useRouter();

  const { known, writable } = useNetworkAccess();

  const reporters = useMemo(() => {
    const out = {} as Record<Part, LoadReport>;
    for (const p of PARTS) {
      out[p] = (ok: boolean) => setPartsFailed((cur) => (!!cur[p] === !ok ? cur : { ...cur, [p]: !ok }));
    }
    return out;
  }, []);

  const loadNetworks = useCallback(async () => {
    setRefreshing(true);
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
      try {
        const o = await api.get<NetworkOverview>("/network/overview");
        const byId: Record<string, OverviewNetwork> = {};
        for (const n of o.networks ?? []) byId[n.id] = n;
        setOverview(byId);
        setBlocked(o.changes_blocked ? (o.blocked_detail ?? "Changes are being refused.") : null);
        setOverviewFailed(false);
      } catch (e) {
        setOverview({});
        setBlocked(null);
        setOverviewFailed(!(e instanceof ApiError && e.status === 404));
      }
      setLoadFailed(false);
      setUpdatedAt(Date.now());
    } catch {
      setLoadFailed(true);
    } finally {
      setRefreshing(false);
    }
  }, []);

  const loadRevisions = useCallback(async () => {
    try {
      const r = await api.get<ListResp<NetRevision>>("/network/revisions");
      const newest = (r.data ?? [])[0] ?? null;
      setPending(newest && newest.state === "pending_confirmation" ? newest : null);
      setRevisionsFailed(false);
    } catch {
      setRevisionsFailed(true);
    }
  }, []);

  const reload = useCallback(() => {
    void loadNetworks();
    void loadRevisions();
    setStagedKey((k) => k + 1);
  }, [loadNetworks, loadRevisions]);

  useEffect(() => {
    reload();
  }, [reload]);

  usePoll(() => loadNetworks(), 30000, { enabled: busy === null && acting === null });

  const left = useSecondsLeft(pending?.confirm_deadline ?? null);
  const reloadedFor = useRef<string | null>(null);
  useEffect(() => {
    if (pending && left === 0 && reloadedFor.current !== pending.id) {
      reloadedFor.current = pending.id;
      const t = setTimeout(() => void inBackground(reload), 2000);
      return () => clearTimeout(t);
    }
  }, [left, pending, reload]);

  const [confirming, setConfirming] = useState<{ kind: "disable" | "delete"; net: GuestNetwork } | null>(null);
  const [confirmErr, setConfirmErr] = useState<string | null>(null);

  async function enableNetwork(net: GuestNetwork) {
    setActing(net.id);
    try {
      await api.post(`/network/guest-networks/${net.id}/enable`);
      toast.success(`${net.name} is staged to switch on`, "Apply the changes on this screen to start it.");
      reload();
    } catch (e) { toast.error(`${net.name} could not be switched on`, netErrorWords(e)); }
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
        kind === "disable" ? `${net.name} is staged to switch off` : `${net.name} is staged for deletion`,
        "Nothing changes for clients until you apply the changes.",
      );
      reload();
    }
    catch (e) { setConfirmErr(netErrorWords(e)); }
    finally { setActing(null); }
  }

  async function onValidate() {
    setBusy("validate"); setErr(null); setHealth(null);
    try {
      const r = await api.post<ValidateResult>("/network/validate");
      setValidation(r.validation);
      if (r.validation.ok) toast.success("Validation passed", "The staged configuration can be applied.");
    } catch (e) { setErr(netErrorWords(e)); }
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
        toast.success("Change applied, confirmation required", "Keep the change before the timer runs out, or it rolls back automatically.");
      } else if (r.state === "rolled_back") {
        setErr("The change was rolled back because the health checks failed. The previous configuration is in place; the results below say what failed.");
      } else if (r.state === "failed") {
        setErr("The change could not be applied. The results below say why; Config history shows the current state.");
      } else {
        toast.success(r.state === "active" ? "Change applied" : revisionStateLabel(r.state));
      }
      reload();
    } catch (e) { if (isReauth(e)) throw e; setErr(netErrorWords(e, true)); }
    finally { setBusy(null); }
  }

  function onConfirm(id: string) {
    stepUp.ask("confirm", async (password) => {
      setBusy("confirm"); setErr(null);
      try { await api.post(`/network/revisions/${id}/confirm`, { password }); toast.success("Configuration kept", "The applied change is now the active configuration."); reload(); }
      catch (e) { if (isReauth(e)) throw e; setErr(netErrorWords(e, true)); }
      finally { setBusy(null); }
    });
  }

  function onRollback(id: string) {
    stepUp.ask("rollback", async (password) => {
      setBusy("rollback"); setErr(null);
      try { await api.post(`/network/revisions/${id}/rollback`, { password }); toast.success("Configuration rolled back", "The previous configuration is back in place."); reload(); }
      catch (e) { if (isReauth(e)) throw e; setErr(netErrorWords(e, true)); }
      finally { setBusy(null); }
    });
  }

  const shownRows = (rows ?? []).filter((n) => !n.archived);
  const enabledCount = servingCount(shownRows, overview);
  const waiting = shownRows.filter((n) => (overview[n.id]?.changes.length ?? 0) > 0).length;
  const archivedCount = (rows ?? []).length - shownRows.length;
  const clientCounts = shownRows
    .map((n) => overview[n.id]?.active_clients ?? status[n.id]?.active_clients)
    .filter((c): c is number => typeof c === "number");
  const clientTotal = clientCounts.reduce((s, x) => s + x, 0);
  const hasResults = !!validation || !!(health && health.length);
  const failedParts = PARTS.filter((p) => partsFailed[p]);
  const retrying = refreshing;

  const newButton = (
    <LinkButton href="/network/new" variant="primary">
      <Plus /> New client network
    </LinkButton>
  );

  const tryAgain = (
    <Button size="sm" variant="secondary" onClick={reload} disabled={retrying}>
      <RefreshCw className={cn(retrying && "animate-spin motion-reduce:animate-none")} /> Try again
    </Button>
  );

  return (
    <PageShell width="wide">
      <PageHeader
        title="Client networks"
        description="The Wi-Fi networks clients join."
        meta={rows !== null && (
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} onRefresh={() => void loadNetworks()} intervalSeconds={30} error={loadFailed && rows !== null} />
        )}
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
                  <>Creating, editing, switching off or deleting a network only <strong>stages</strong> the change.</>,
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
            <Button variant={waiting > 0 ? "primary" : "secondary"} disabled={busy !== null} onClick={onApply}>
              <Power /> {busy === "apply" ? "Applying…" : waiting > 0 ? `Apply changes (${waiting.toLocaleString()})` : "Apply changes"}
              {waiting > 0 && busy !== "apply" && <span className="sr-only">{`, ${plural(waiting, "network")} with changes not applied`}</span>}
            </Button>
            <LinkButton variant="secondary" href="/network/recovery">Recovery</LinkButton>
            {newButton}
          </>
        )}
      />

      {known && !writable && <ReadOnlyNotice>Your role can view client networks but not change them.</ReadOnlyNotice>}

      {err && (
        <div className={FADE}>
          <Callout tone="danger">{err}</Callout>
        </div>
      )}

      {revisionsFailed && (
        <div className={FADE}>
          <Callout tone="warning" title="Whether a change is waiting to be kept could not be checked">
            <p>
              If an applied change is waiting, it rolls back on its own when its timer runs out. Try again, or open{" "}
              <Link href="/network/revisions" className="font-medium underline underline-offset-2">Config history</Link>.
            </p>
            <div className="mt-2">{tryAgain}</div>
          </Callout>
        </div>
      )}

      {rows !== null && overviewFailed && (
        <div className={FADE}>
          <Callout tone="info" title="Live status could not be read">
            The status column shows saved settings only, and the number on Apply changes may leave some changes out.
          </Callout>
        </div>
      )}

      {failedParts.length > 0 && (
        <div className={FADE}>
          <Callout tone="warning" title="Part of this page could not be loaded">
            <p>{listWords(failedParts)} could not be loaded, so they may be missing below.</p>
            <div className="mt-2">{tryAgain}</div>
          </Callout>
        </div>
      )}

      {blocked && (
        <div className={FADE}>
          <Callout tone="warning" title="Changes are being refused">
            {blocked} The appliance re-checks by itself; once it is back on its confirmed configuration this clears.
          </Callout>
        </div>
      )}

      {pending && (
        <PendingChangeBanner
          title={`Revision #${pending.seq}: keep it, or it rolls back automatically`}
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
          {hasResults ? <div className={FADE}><ApplyResults validation={validation} health={health} /></div> : undefined}
        </PendingChangeBanner>
      )}

      {!pending && hasResults && (
        <SectionCard className={FADE} title="Validation & health">
          <ApplyResults validation={validation} health={health} />
        </SectionCard>
      )}

      <FactoryNetworkCard key={`f${stagedKey}`} writable={writable} onStaged={reload} onLoaded={reporters["Factory network"]} />
      <StagedChangesList writable={writable} reloadKey={stagedKey} onCancelled={reload} onLoaded={reporters["Staged changes"]} />
      <DefaultNetworkCard reloadKey={stagedKey} onLoaded={reporters["Default Network"]} />

      {rows !== null && rows.length > 0 && (
        <StatGrid cols={3}>
          <StatCard label="Client networks" value={shownRows.length.toLocaleString()} hint={archivedCount > 0 ? `${archivedCount.toLocaleString()} archived` : undefined} icon={<Network />} />
          <StatCard
            label="Serving clients"
            value={enabledCount.toLocaleString()}
            hint={shownRows.length - enabledCount > 0 ? `${(shownRows.length - enabledCount).toLocaleString()} not serving` : "All serving"}
            icon={<Wifi />}
            tone={enabledCount === 0 ? "warn" : "default"}
          />
          <StatCard
            label="Devices connected"
            value={clientCounts.length ? clientTotal.toLocaleString() : "Not known"}
            hint="On all client networks, now"
            icon={<Users />}
          />
        </StatGrid>
      )}

      <Card className="overflow-hidden">
        {rows === null && loadFailed ? (
          <div role="alert" className={FADE}>
            <EmptyState
              icon={<AlertTriangle />}
              title="The client networks could not be loaded"
              hint="Check the appliance is reachable, then try again. Nothing has been changed."
              action={
                <Button variant="secondary" onClick={() => void loadNetworks()} disabled={refreshing}>
                  <RefreshCw className={cn(refreshing && "animate-spin motion-reduce:animate-none")} /> Try again
                </Button>
              }
            />
          </div>
        ) : rows === null ? (
          <SkeletonRows rows={4} cols={6} />
        ) : shownRows.length === 0 ? (
          <EmptyState
            icon={<Network />}
            title="No client networks yet"
            hint="Create one to give clients Wi-Fi with a sign-in page, addresses and internet access."
            action={writable ? newButton : undefined}
          />
        ) : (
          <Table label="Client networks" aria-label="Client networks">
            <THead>
              <TR>
                <TH>Name</TH>
                <TH>Type</TH>
                <TH className="hidden 2xl:table-cell">Parent interface</TH>
                <TH className="hidden md:table-cell">Gateway / subnet</TH>
                <TH className="hidden 2xl:table-cell">DHCP</TH>
                <TH className="hidden 2xl:table-cell">Pool</TH>
                <TH className="hidden sm:table-cell">Portal</TH>
                <TH className="hidden lg:table-cell">Room sign-in</TH>
                <TH>Status</TH>
                <TH numeric>Clients</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {shownRows.map((n) => {
                const clients = overview[n.id]?.active_clients ?? status[n.id]?.active_clients;
                return (
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
                      <Badge tone={n.network_type === "vlan" ? "info" : "default"} title={topologyWords(n).detail}>{networkTypeLabel(n)}</Badge>
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
                    <TD className="hidden max-w-[12rem] lg:table-cell">
                      <RoomSignInCell brief={overview[n.id]?.room_sign_in} />
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
                    <TD numeric muted>{typeof clients === "number" ? clients.toLocaleString() : "Not known"}</TD>
                    <TD className="w-0">
                      <RowActions
                        subject={n.name}
                        primary={{ label: writable ? "Edit" : "View", href: `/network/${n.id}` }}
                        items={[
                          {
                            label: "Switch off",
                            hidden: !writable || !n.enabled,
                            disabled: acting === n.id,
                            onSelect: () => { setConfirmErr(null); setConfirming({ kind: "disable", net: n }); },
                          },
                          {
                            label: "Switch on",
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
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>

      <PortModeCard writable={writable} reloadKey={stagedKey} onStaged={reload} onLoaded={reporters["Client ports"]} />
      <ArchivedNetworks writable={writable} reloadKey={stagedKey} onRestored={reload} onLoaded={reporters["Archived networks"]} />

      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(v) => !v && setConfirming(null)}
        title={
          confirming?.kind === "delete"
            ? `Delete the network ${confirming.net.name}?`
            : `Switch off ${confirming?.net.name ?? ""}?`
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
                "The network is marked switched off in the staged configuration.",
                "Clients on it stay connected until you apply the change; after that, nobody can join it.",
              ]
        }
        confirmLabel={confirming?.kind === "delete" ? "Delete network" : "Switch off"}
        confirmVariant="danger"
        busy={acting !== null && acting === confirming?.net.id}
        error={confirmErr}
        onConfirm={applyStagedRemoval}
      />
      {stepUp.dialog}
    </PageShell>
  );
}

function RoomSignInCell({ brief }: { brief?: RoomSignInBrief }) {
  if (!brief) return <span className="text-muted-foreground">Not assessed</span>;
  const st = STATE_WORDS[brief.state];
  return (
    <div title={brief.summary}>
      <Badge tone={st.tone} dot>{st.label}</Badge>
      <div className="truncate text-xs text-muted-foreground">
        {POLICY_WORDS[brief.policy]}{brief.interfaces.length > 0 ? `: ${brief.interfaces.join(", ")}` : ""}
      </div>
    </div>
  );
}
