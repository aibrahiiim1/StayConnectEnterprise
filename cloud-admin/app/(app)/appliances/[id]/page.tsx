"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import {
  ArrowRightLeft, Ban, ChevronDown, Download, Pause, Play, Power, RefreshCw, Trash2,
} from "lucide-react";
import {
  api, ApiError, withStepUp, type ActivityEvent, type ApplianceDetail, type LicenseRow,
} from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Skeleton, Switch } from "@/components/ui/misc";
import { LiveStatus } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { NotAvailable } from "@/components/ui/patterns";
import { DeleteDialog } from "@/components/delete-dialog";
import { ActivateDialog, MoveDialog, SetLicenseDialog } from "@/components/appliance-dialogs";
import { ActivationBadge, ConnectionBadge, Fact, LicenseBadge } from "@/components/status-badge";
import { usePermissions } from "@/lib/permissions";
import { usePoll } from "@/lib/use-poll";
import { saveFile } from "@/lib/download";
import {
  actionTone, actionWords, activationInfo, ago, connectionSentence, daysUntil, formatDateTime, formatDay, formatLicenseDay,
  licenseInfo,
} from "@/lib/status";
import { cn } from "@/lib/utils";

type ReasonAction = {
  title: string;
  description: React.ReactNode;
  confirmLabel: string;
  danger?: boolean;
  url: string;
  done: string;
  consequences?: string[];
  /** Typed confirmation: the serial. */
  typed?: boolean;
};

const OFFLINE_VALID_HOURS = 168;

export default function AppliancePage({ params }: { params: { id: string } }) {
  const id = params.id;
  const router = useRouter();
  const toast = useToast();
  const { can } = usePermissions();
  const [a, setA] = useState<ApplianceDetail | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [missing, setMissing] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  const [dialog, setDialog] = useState<"activate" | "license" | "move" | "retire" | "delete" | null>(null);
  const [reasonAction, setReasonAction] = useState<ReasonAction | null>(null);
  const [actionBusy, setActionBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);
  const [emergency, setEmergency] = useState(false);
  const [fileBusy, setFileBusy] = useState(false);

  const load = useCallback(async () => {
    setRefreshing(true);
    try {
      setA(await api.get<ApplianceDetail>(`/cloud/v1/appliances/${id}`));
      setUpdatedAt(Date.now());
      setErr(null);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) setMissing(true);
      else setErr(e);
    } finally {
      setRefreshing(false);
    }
  }, [id]);

  useEffect(() => { void load(); }, [load]);
  // While it is activating, look every 5 seconds: the operator is usually watching for it to finish.
  const activating = a?.activation === "activating";
  usePoll(load, activating ? 5_000 : 30_000, !!a);

  if (missing) {
    return (
      <PageShell>
        <PageHeader title="Appliance not found" />
        <NotAvailable
          title="This appliance does not exist"
          reason={<>It may have been deleted, or it belongs to a customer you cannot see. <Link className="underline" href="/appliances">Back to appliances</Link></>}
        />
      </PageShell>
    );
  }

  if (!a) {
    return (
      <PageShell>
        <ErrorBanner err={err} />
        <div className="space-y-4" aria-busy="true">
          <span className="sr-only">Loading</span>
          <Skeleton className="h-10 w-64" />
          <Skeleton className="h-40" />
          <Skeleton className="h-64" />
        </div>
      </PageShell>
    );
  }

  const lic = a.license ?? { state: "none" as const };
  const current: LicenseRow | undefined =
    a.licenses?.find((l) => l.id && l.id === lic.id) ?? a.licenses?.[0];
  const hasLicense = lic.state !== "none";
  const canLicense = can["licenses.manage"] && (a.activation === "activated" || a.activation === "activating");
  const canManage = can["appliances.manage"];
  const needsLicense = ["none", "expiring", "grace", "expired", "revoked"].includes(lic.state);

  async function runReasonAction({ reason }: { reason: string }) {
    const r = reasonAction;
    if (!r || !a) return;
    setActionBusy(true);
    setActionErr(null);
    try {
      await withStepUp(() => api.post(r.url, { reason }));
      toast.success(r.done, a.serial);
      setReasonAction(null);
      await load();
    } catch (e) {
      setActionErr(e);
    } finally {
      setActionBusy(false);
    }
  }

  async function retire({ reason }: { reason: string }) {
    if (!a) return;
    setActionBusy(true);
    setActionErr(null);
    try {
      await withStepUp(() =>
        api.post(`/cloud/v1/appliances/${a.id}/retire`, { reason, emergency, confirm_serial: a.serial }),
      );
      toast.success(emergency ? `${a.serial} retired` : `Retiring ${a.serial}`, emergency ? undefined : "Waiting for the appliance to confirm.");
      setDialog(null);
      await load();
    } catch (e) {
      setActionErr(e);
    } finally {
      setActionBusy(false);
    }
  }

  async function download(kind: "license" | "package") {
    if (!a) return;
    setFileBusy(true);
    try {
      if (kind === "license") {
        const res = await withStepUp(() =>
          api.post<unknown>(`/cloud/v1/appliances/${a.id}/offline-license`, { valid_hours: OFFLINE_VALID_HOURS }),
        );
        saveFile(`license-${a.serial}.json`, res);
        toast.success("License file downloaded", "Upload it in Hotel Admin under Appliance & licence.");
      } else {
        const res = await withStepUp(() =>
          api.post<unknown>(`/cloud/v1/appliances/${a.id}/offline-activation-package`, { valid_hours: OFFLINE_VALID_HOURS }),
        );
        saveFile(`activation-package-${a.serial}.json`, res);
        toast.success("Activation package downloaded", "Upload it in Hotel Admin under Appliance & licence. Valid for 7 days.");
      }
    } catch (e) {
      toast.error("Download failed", e instanceof Error ? e.message : String(e));
    } finally {
      setFileBusy(false);
    }
  }

  const licenseAction = (verb: "suspend" | "resume" | "revoke"): ReasonAction => ({
    suspend: {
      title: "Suspend license",
      description: "Guest access on this appliance stops until the license is resumed.",
      confirmLabel: "Suspend license",
      danger: true,
      url: `/cloud/v1/licenses/${lic.id}/suspend`,
      done: "License suspended",
    },
    resume: {
      title: "Resume license",
      description: "Guest access is restored on the appliance's next contact.",
      confirmLabel: "Resume license",
      url: `/cloud/v1/licenses/${lic.id}/resume`,
      done: "License resumed",
    },
    revoke: {
      title: "Revoke license",
      description: "The license is cancelled for good. To restore service you will need to set a new license.",
      confirmLabel: "Revoke license",
      danger: true,
      typed: true,
      url: `/cloud/v1/licenses/${lic.id}/revoke`,
      done: "License revoked",
      consequences: ["Guest access on this appliance stops.", "This license cannot be resumed."],
    },
  })[verb];

  const activationText = activationInfo(a.activation);

  return (
    <PageShell>
      <PageHeader
        title={<span className="font-mono">{a.serial}</span>}
        description={[a.hostname, a.model, a.version ? `version ${a.version}` : null].filter(Boolean).join(" · ") || undefined}
        actions={
          <LiveStatus
            updatedAt={updatedAt}
            refreshing={refreshing}
            onRefresh={load}
            intervalSeconds={activating ? 5 : 30}
            error={!!err}
          />
        }
      >
        <div className="flex flex-wrap items-center gap-2" aria-label="Status">
          <ActivationBadge value={a.activation} />
          <ConnectionBadge value={a.connection} />
          <LicenseBadge value={lic.state} />
          {a.open_alerts ? (
            <Link href="/system/security-alerts" className="text-caption font-medium text-destructive underline-offset-2 hover:underline">
              {a.open_alerts} open security {a.open_alerts === 1 ? "alert" : "alerts"}
            </Link>
          ) : null}
        </div>
      </PageHeader>

      <ErrorBanner err={err} />

      {/* THE ONE THING TO DO NEXT, by state. */}
      {a.activation === "waiting" && (
        <Card className="border-info/40">
          <CardHeader>
            <div className="space-y-1">
              <CardTitle>Waiting for activation</CardTitle>
              <CardDescription>
                Registered {ago(a.registered_at)}. Activate it to choose its customer, site and license.
              </CardDescription>
            </div>
            {can["appliances.activate"] && (
              <Button onClick={() => setDialog("activate")}>Activate</Button>
            )}
          </CardHeader>
          {!can["appliances.activate"] && (
            <CardBody className="text-sm text-muted-foreground">A platform admin can activate it.</CardBody>
          )}
        </Card>
      )}

      {a.activation === "activating" && (
        <Card className="border-info/40">
          <CardHeader>
            <div className="space-y-1">
              <CardTitle>Activating</CardTitle>
              <CardDescription>
                {activationText.explain} This page checks every 5 seconds.
              </CardDescription>
            </div>
          </CardHeader>
          {canManage && (
            <CardFooter>
              <span className="me-auto text-caption text-muted-foreground">
                No internet at the site? Download the activation package and upload it in the appliance&apos;s Hotel Admin.
              </span>
              <Button variant="secondary" size="sm" disabled={fileBusy} onClick={() => download("package")}>
                <Download /> Activation package
              </Button>
            </CardFooter>
          )}
        </Card>
      )}

      {a.activation === "retiring" && (
        <Callout tone="warning" title="Retiring">
          Retirement is signed; waiting for the appliance to confirm
          {a.retirement?.deadline ? <> (by {formatDateTime(a.retirement.deadline)})</> : null}.{" "}
          {canManage && (
            <Button
              variant="link"
              className="h-auto p-0 align-baseline"
              onClick={() => { setEmergency(true); setActionErr(null); setDialog("retire"); }}
            >
              Retire now without waiting
            </Button>
          )}
        </Callout>
      )}

      {a.activation === "retired" && (
        <Card>
          <CardHeader>
            <div className="space-y-1">
              <CardTitle>Retired</CardTitle>
              <CardDescription>{activationText.explain} Its history stays in the audit log.</CardDescription>
            </div>
            {canManage && (
              <Button variant="secondary" onClick={() => setDialog("delete")}>
                <Trash2 /> Delete record
              </Button>
            )}
          </CardHeader>
        </Card>
      )}

      {a.replacement?.pending && (
        <Callout tone="info" title="Marked for replacement">
          Activating a new appliance at the same site retires this one
          {a.replacement.deadline ? <> (before {formatDay(a.replacement.deadline)})</> : null}.
        </Callout>
      )}

      <div className="grid gap-5 lg:grid-cols-3">
        <div className="min-w-0 space-y-5 lg:col-span-2">
          {a.activation !== "waiting" && (
            <Card>
              <CardHeader>
                <div className="flex items-center gap-2.5">
                  <CardTitle>License</CardTitle>
                  <LicenseBadge value={lic.state} />
                </div>
                {canLicense && (
                  <Button variant={needsLicense ? "primary" : "secondary"} size="sm" onClick={() => setDialog("license")}>
                    {hasLicense ? "Renew or change" : "Issue license"}
                  </Button>
                )}
              </CardHeader>
              <CardBody>
                {hasLicense ? (
                  <dl className="grid gap-4 sm:grid-cols-3">
                    <Fact label="Guests online at once">
                      <span className="tabular">{(lic.max_concurrent_online_guests ?? current?.max_concurrent_online_guests)?.toLocaleString() ?? "—"}</span>
                    </Fact>
                    <Fact label="Valid until">
                      {formatLicenseDay(lic.valid_until)}
                      {lic.valid_until && <div className="text-caption text-muted-foreground">{daysLeftWords(lic.valid_until)}</div>}
                    </Fact>
                    <Fact label="Grace period">
                      {current?.grace_period_days != null ? `${current.grace_period_days} days` : "—"}
                      {lic.grace_ends_at && (
                        <div className="text-caption text-muted-foreground">ends {formatLicenseDay(lic.grace_ends_at)}</div>
                      )}
                    </Fact>
                  </dl>
                ) : (
                  <p className="text-sm text-muted-foreground">
                    {licenseInfo("none").explain}
                    {a.activation === "activated" && " Guests cannot get online until it has one."}
                  </p>
                )}
              </CardBody>
              {canLicense && hasLicense && lic.id && (
                <CardFooter>
                  {(lic.state === "active" || lic.state === "expiring" || lic.state === "grace") && (
                    <Button variant="ghost" size="sm" onClick={() => { setActionErr(null); setReasonAction(licenseAction("suspend")); }}>
                      <Pause /> Suspend
                    </Button>
                  )}
                  {lic.state === "suspended" && (
                    <Button variant="secondary" size="sm" onClick={() => { setActionErr(null); setReasonAction(licenseAction("resume")); }}>
                      <Play /> Resume
                    </Button>
                  )}
                  {lic.state !== "revoked" && (
                    <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => { setActionErr(null); setReasonAction(licenseAction("revoke")); }}>
                      <Ban /> Revoke
                    </Button>
                  )}
                  {lic.state !== "revoked" && (
                    <Button variant="ghost" size="sm" className="ms-auto" disabled={fileBusy} onClick={() => download("license")}>
                      <Download /> Offline license file
                    </Button>
                  )}
                </CardFooter>
              )}
            </Card>
          )}

          {(a.licenses?.length ?? 0) > 0 && (
            <Card>
              <CardHeader><CardTitle>License history</CardTitle></CardHeader>
              <Table aria-label="License history">
                <THead>
                  <TR>
                    <TH>Version</TH><TH>State</TH><TH className="hidden sm:table-cell">Guests</TH>
                    <TH>Valid until</TH><TH className="hidden md:table-cell">Issued</TH>
                  </TR>
                </THead>
                <tbody>
                  {a.licenses!.map((l) => (
                    <TR key={l.id}>
                      <TD className="tabular">v{l.license_version ?? "—"}</TD>
                      <TD><LicenseBadge value={l.state} /></TD>
                      <TD className="hidden tabular sm:table-cell">{l.max_concurrent_online_guests?.toLocaleString() ?? "—"}</TD>
                      <TD>{formatLicenseDay(l.valid_until)}</TD>
                      <TD className="hidden text-muted-foreground md:table-cell">{formatDay(l.issued_at)}</TD>
                    </TR>
                  ))}
                </tbody>
              </Table>
            </Card>
          )}

          <Card>
            <CardHeader>
              <CardTitle>Activity</CardTitle>
              {can["system.read"] && (
                <Link href={`/system/audit?appliance_id=${a.id}`} className="text-sm text-primary underline-offset-2 hover:underline">
                  Full audit log
                </Link>
              )}
            </CardHeader>
            <ActivityList events={a.events ?? []} />
          </Card>
        </div>

        <div className="min-w-0 space-y-5">
          <Card>
            <CardHeader>
              <CardTitle>Installed at</CardTitle>
              {canManage && a.activation === "activated" && (
                <Button variant="ghost" size="sm" onClick={() => setDialog("move")}>
                  <ArrowRightLeft /> Move
                </Button>
              )}
            </CardHeader>
            <CardBody>
              <dl className="space-y-3">
                <Fact label="Customer">
                  {a.customer_id ? (
                    <Link href={`/customers/${a.customer_id}`} className="font-medium text-primary underline-offset-2 hover:underline">
                      {a.customer_name ?? "Customer"}
                    </Link>
                  ) : "Not assigned yet"}
                </Fact>
                <Fact label="Site">{a.site_name ?? "—"}</Fact>
              </dl>
            </CardBody>
          </Card>

          <Card>
            <CardHeader><CardTitle>Appliance</CardTitle></CardHeader>
            <CardBody>
              <dl className="space-y-3">
                <Fact label="Connection">{connectionSentence(a)}</Fact>
                <Fact label="Registered">{formatDateTime(a.registered_at)}</Fact>
                {a.activated_at && <Fact label="Activated">{formatDateTime(a.activated_at)}</Fact>}
                {a.last_public_ip && <Fact label="Last address"><span className="font-mono text-xs">{a.last_public_ip}</span></Fact>}
                {a.version && <Fact label="Software version">{a.version}</Fact>}
              </dl>
            </CardBody>
            {canManage && (a.activation === "activated" || a.activation === "waiting") && (
              <CardFooter>
                {a.activation === "activated" ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-destructive hover:text-destructive"
                    onClick={() => { setEmergency(false); setActionErr(null); setDialog("retire"); }}
                  >
                    <Power /> Retire appliance
                  </Button>
                ) : (
                  <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setDialog("delete")}>
                    <Trash2 /> Delete record
                  </Button>
                )}
              </CardFooter>
            )}
          </Card>

          <Advanced
            a={a}
            canManage={canManage}
            fileBusy={fileBusy}
            onPackage={() => download("package")}
            onAction={(r) => { setActionErr(null); setReasonAction(r); }}
          />
        </div>
      </div>

      {dialog === "activate" && (
        <ActivateDialog appliance={a} open onOpenChange={(v) => { if (!v) setDialog(null); }} onDone={load} />
      )}
      {dialog === "license" && (
        <SetLicenseDialog appliance={a} open onOpenChange={(v) => { if (!v) setDialog(null); }} onDone={load} />
      )}
      {dialog === "move" && (
        <MoveDialog appliance={a} open onOpenChange={(v) => { if (!v) setDialog(null); }} onDone={load} />
      )}

      <ConfirmDialog
        open={dialog === "retire"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title={`Retire ${a.serial}`}
        description="The appliance stops serving guests and can no longer connect to Central."
        confirmLabel={emergency ? "Retire now" : "Retire appliance"}
        confirmVariant="danger"
        busy={actionBusy}
        error={actionErr}
        consequences={[
          "Its license and certificate stop working.",
          emergency
            ? "It is retired immediately, without waiting for the appliance to confirm."
            : "It is retired once the appliance confirms, normally within a minute.",
          "It cannot be undone. Only its record can then be deleted.",
        ]}
        confirmText={a.serial}
        confirmTextLabel="Type the serial"
        requireReason
        onConfirm={retire}
      >
        <label className="flex items-start gap-3 text-sm">
          <Switch checked={emergency} onCheckedChange={setEmergency} label="Emergency retire" />
          <span>
            <span className="font-medium">Emergency: don&apos;t wait for the appliance</span>
            <span className="block text-caption text-muted-foreground">For a lost, stolen or dead appliance.</span>
          </span>
        </label>
      </ConfirmDialog>

      <ConfirmDialog
        open={!!reasonAction}
        onOpenChange={(v) => { if (!v) setReasonAction(null); }}
        title={reasonAction?.title ?? ""}
        description={reasonAction?.description}
        confirmLabel={reasonAction?.confirmLabel}
        confirmVariant={reasonAction?.danger ? "danger" : "primary"}
        busy={actionBusy}
        error={actionErr}
        consequences={reasonAction?.consequences}
        confirmText={reasonAction?.typed ? a.serial : undefined}
        confirmTextLabel="Type the serial"
        requireReason
        onConfirm={runReasonAction}
      />

      <DeleteDialog
        open={dialog === "delete"}
        onClose={() => setDialog(null)}
        onDeleted={() => { toast.success(`${a.serial} deleted`); router.push("/appliances"); }}
        title={`Delete ${a.serial}`}
        what="Appliance record"
        expected={a.serial}
        confirmHint="Type the serial"
        confirmField="confirm_serial"
        deleteUrl={`/cloud/v1/appliances/${a.id}`}
        consequences={[
          a.activation === "waiting"
            ? "If it is still powered on, it registers again and reappears here."
            : "The record is removed; its audit history is kept.",
          "It cannot be undone.",
        ]}
      />
    </PageShell>
  );
}

// "12 days left", "ends today", "ended 3 days ago" -- the date itself is already on the line above.
function daysLeftWords(until: string): string {
  const days = Math.ceil((Date.parse(until) - Date.now()) / 86_400_000);
  if (Number.isNaN(days)) return "";
  if (days > 1) return `${days.toLocaleString()} days left`;
  if (days === 1) return "1 day left";
  if (days === 0) return "ends today";
  return days === -1 ? "ended yesterday" : `ended ${(-days).toLocaleString()} days ago`;
}

// The same event repeated back to back (an appliance retrying with an expired token, say) is one line with a
// count, not a screen of identical rows that pushes everything else out of view.
type GroupedEvent = ActivityEvent & { count: number; first_at?: string };
function groupRepeats(events: ActivityEvent[]): GroupedEvent[] {
  const out: GroupedEvent[] = [];
  for (const e of events) {
    const prev = out[out.length - 1];
    const key = (x: ActivityEvent) => [x.action, x.actor_email ?? x.actor, x.reason ?? x.detail].join("|");
    if (prev && key(prev) === key(e)) {
      prev.count += 1;
      prev.first_at = e.ts ?? e.at;
    } else out.push({ ...e, count: 1 });
  }
  return out;
}

function ActivityList({ events }: { events: ActivityEvent[] }) {
  if (events.length === 0) return <EmptyState title="No activity yet" className="py-8" />;
  return (
    <ol className="divide-y divide-border" aria-label="Recent activity">
      {groupRepeats(events).map((e, i) => {
        const at = e.ts ?? e.at;
        const tone = actionTone(e.action);
        return (
          <li key={i} className="flex gap-3 px-5 py-3">
            <span
              className={cn(
                "mt-1.5 size-2 shrink-0 rounded-full",
                tone === "ok" ? "bg-success" : tone === "warn" ? "bg-warning" : tone === "err" ? "bg-destructive" : "bg-muted-foreground/50",
              )}
              aria-hidden
            />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-baseline justify-between gap-x-3">
                <span className="text-sm font-medium">
                  {actionWords(e.action)}
                  {e.count > 1 && (
                    <span className="ml-1.5 font-normal text-muted-foreground" title={`${e.count} times, first ${formatDateTime(e.first_at)}`}>
                      ×{e.count}
                    </span>
                  )}
                </span>
                <time className="text-caption text-muted-foreground" dateTime={at} title={formatDateTime(at)}>{ago(at)}</time>
              </div>
              {(e.actor_email || e.actor || e.reason || e.detail) && (
                <div className="text-caption text-muted-foreground">
                  {[e.actor_email ?? e.actor, e.reason ?? e.detail].filter(Boolean).join(" · ")}
                </div>
              )}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

/** Rarely needed, so collapsed: repair actions and the protocol-level facts. */
function Advanced({
  a, canManage, fileBusy, onPackage, onAction,
}: {
  a: ApplianceDetail;
  canManage: boolean;
  fileBusy: boolean;
  onPackage: () => void;
  onAction: (r: ReasonAction) => void;
}) {
  const live = a.activation === "activated" || a.activation === "activating";
  const certDays = daysUntil(a.identity?.cert_not_after);
  return (
    <details className="group rounded-lg border border-border bg-card shadow-card">
      <summary
        className={cn(
          "flex cursor-pointer list-none items-center justify-between gap-3 rounded-lg px-5 py-4 text-emphasis",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden",
        )}
      >
        Advanced
        <ChevronDown className="size-4 text-muted-foreground transition-transform group-open:rotate-180 motion-reduce:transition-none" aria-hidden />
      </summary>
      <div className="space-y-5 border-t border-border px-5 py-4">
        {canManage && live && (
          <div className="space-y-2">
            <div className="text-label">Repair</div>
            <div className="flex flex-col items-start gap-1">
              <Button variant="ghost" size="sm" onClick={() => onAction({
                title: "Reissue certificate",
                description: "Issues the appliance a fresh certificate. Use it when the appliance reports a certificate problem.",
                confirmLabel: "Reissue certificate",
                url: `/cloud/v1/appliances/${a.id}/reissue-certificate`,
                done: "Certificate reissued",
              })}>
                <RefreshCw /> Reissue certificate
              </Button>
              <Button variant="ghost" size="sm" onClick={() => onAction({
                title: "Rebind WAN MAC",
                description: "After a network card change, binds the license to the new WAN MAC. The license terms stay the same.",
                confirmLabel: "Rebind WAN MAC",
                url: `/cloud/v1/appliances/${a.id}/rebind-wan-mac`,
                done: "WAN MAC rebound",
              })}>
                <RefreshCw /> Rebind WAN MAC
              </Button>
              {!a.replacement?.pending && (
                <Button variant="ghost" size="sm" onClick={() => onAction({
                  title: "Mark for replacement",
                  description: "Activating a new appliance at the same site will retire this one and take over its place.",
                  confirmLabel: "Mark for replacement",
                  url: `/cloud/v1/appliances/${a.id}/replace`,
                  done: "Marked for replacement",
                })}>
                  <ArrowRightLeft /> Mark for replacement
                </Button>
              )}
              <Button variant="ghost" size="sm" disabled={fileBusy} onClick={onPackage}>
                <Download /> Offline activation package
              </Button>
            </div>
          </div>
        )}

        <div className="space-y-2">
          <div className="text-label">Technical details</div>
          <dl className="grid gap-3 text-xs">
            <Tech label="Appliance ID" value={a.id} />
            <Tech label="WAN MAC" value={a.identity?.wan_mac} />
            <Tech label="LAN MAC" value={a.identity?.lan_mac} />
            <Tech label="Hardware fingerprint" value={a.identity?.hardware_fingerprint} />
            <Tech label="Identity key fingerprint" value={a.identity?.identity_key_fingerprint} />
            <Tech label="Certificate fingerprint" value={a.identity?.cert_fingerprint} />
            <Tech
              label="Certificate expires"
              value={a.identity?.cert_not_after ? `${formatDay(a.identity.cert_not_after)}${certDays !== null ? ` (${certDays} days)` : ""}` : null}
              plain
            />
            <Tech
              label="Assignment version"
              value={a.assignment?.version != null
                ? `v${a.assignment.version}${a.assignment.acked_version != null ? ` · appliance confirmed v${a.assignment.acked_version}` : ""}`
                : null}
              plain
            />
            <Tech label="Assignment signing key" value={a.assignment?.signer_key_id} />
            <Tech label="License version" value={a.license?.license_version != null ? `v${a.license.license_version}` : null} plain />
          </dl>
        </div>
      </div>
    </details>
  );
}

function Tech({ label, value, plain }: { label: string; value?: string | null; plain?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={cn("break-all", !plain && "font-mono")}>{value || "—"}</dd>
    </div>
  );
}
