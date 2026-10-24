"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import {
  ArrowRightLeft, Ban, ChevronDown, Download, Pause, Play, Power, RefreshCw, Trash2,
} from "lucide-react";
import {
  api, ApiError, withStepUp, type ActivityEvent, type ApplianceDetail, type LicenseRow, type RequestOptions,
} from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle, SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { HistoryList, type HistoryItem } from "@/components/ui/history";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Skeleton, Switch } from "@/components/ui/misc";
import { LiveStatus, NotAvailable } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { HelpList, HelpSection } from "@/components/help";
import { DeleteDialog } from "@/components/delete-dialog";
import { ActivateDialog, MoveDialog, SetLicenseDialog } from "@/components/appliance-dialogs";
import { ActivationBadge, ConnectionBadge, LicenseBadge } from "@/components/status-badge";
import { PayloadView, hasPayload } from "@/components/record-view";
import { usePermissions } from "@/lib/permissions";
import { usePoll } from "@/lib/use-poll";
import { saveFile } from "@/lib/download";
import {
  actionTone, actionWords, activationInfo, ago, connectionSentence, daysUntil, formatDateTime, formatDay, formatLicenseDay,
  licenseInfo, type Tone,
} from "@/lib/status";
import { cn } from "@/lib/utils";
import { siteTypeLabel } from "@/lib/site-types";
import { modulesSummary } from "@/components/license-table";

type ReasonAction = {
  title: string;
  description: React.ReactNode;
  confirmLabel: string;
  danger?: boolean;
  url: string;
  done: string;
  consequences?: string[];
  typed?: boolean;
};

const OFFLINE_VALID_HOURS = 168;
const DANGER_GHOST = "text-destructive hover:bg-destructive-subtle hover:text-destructive";
const LINK = "font-medium text-primary underline-offset-2 hover:underline";

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
  const [version, setVersion] = useState<LicenseRow | null>(null);
  const [event, setEvent] = useState<GroupedEvent | null>(null);

  const load = useCallback(async (opts?: RequestOptions) => {
    setRefreshing(true);
    try {
      setA(await api.get<ApplianceDetail>(`/cloud/v1/appliances/${id}`, opts));
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
        <div className="space-y-5" aria-busy="true">
          <span className="sr-only">Loading</span>
          <Skeleton className="h-9 w-64" />
          <div className="grid gap-5 lg:grid-cols-3">
            <Skeleton className="h-64 lg:col-span-2" />
            <Skeleton className="h-64" />
          </div>
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
        toast.success("License file downloaded", "Upload it in the Admin Console under Appliance & licence.");
      } else {
        const res = await withStepUp(() =>
          api.post<unknown>(`/cloud/v1/appliances/${a.id}/offline-activation-package`, { valid_hours: OFFLINE_VALID_HOURS }),
        );
        saveFile(`activation-package-${a.serial}.json`, res);
        toast.success("Activation package downloaded", "Upload it in the Admin Console under Appliance & licence. Valid for 7 days.");
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
      description: "Client access on this appliance stops until the license is resumed.",
      confirmLabel: "Suspend license",
      danger: true,
      url: `/cloud/v1/licenses/${lic.id}/suspend`,
      done: "License suspended",
    },
    resume: {
      title: "Resume license",
      description: "Client access is restored on the appliance's next contact.",
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
      consequences: ["Client access on this appliance stops.", "This license cannot be resumed."],
    },
  })[verb];

  const openReason = (r: ReasonAction) => { setActionErr(null); setReasonAction(r); };
  const openRetire = (now: boolean) => { setEmergency(now); setActionErr(null); setDialog("retire"); };

  const activationText = activationInfo(a.activation);
  // Missing acknowledgement or a passed deadline both mean retirement remains unconfirmed.
  const unconfirmed =
    a.activation === "retiring" &&
    (a.retirement?.state === "terminal_delivery_failed" ||
      (!!a.retirement?.deadline && Date.parse(a.retirement.deadline) < Date.now()));

  const versions = a.licenses ?? [];
  const events = groupRepeats(a.events ?? []);

  return (
    <PageShell>
      <PageHeader
        title={<span className="font-mono">{a.serial}</span>}
        helpTitle="Appliance"
        meta={
          <div className="flex flex-wrap items-center gap-1.5" aria-label="Status">
            <ActivationBadge value={a.activation} />
            <ConnectionBadge value={a.connection} />
            <LicenseBadge value={lic.state} />
          </div>
        }
        description={[a.hostname, a.model, a.version ? `version ${a.version}` : null].filter(Boolean).join(" · ") || undefined}
        help={
          <>
            <HelpSection title="Lifecycle">
              <HelpList
                items={[
                  <><strong>Waiting for activation</strong>: registered itself; activate it to choose its customer, site and license.</>,
                  <><strong>Activating</strong>: activated here; it picks up its certificate and license on its next contact.</>,
                  <><strong>Retiring</strong> then <strong>Retired</strong>: taken out of service once the appliance confirms, or at once in an emergency.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Without internet at the site">
              <p>
                Download the activation request from the appliance&apos;s Admin Console and import it on the Appliances page.
                The activation package and an offline license file can then be downloaded here.
              </p>
            </HelpSection>
            <HelpSection title="Repair">
              <p>
                <strong>Advanced</strong> reissues the certificate, rebinds the license to a new WAN MAC after a network card
                change, or marks the appliance for replacement by new hardware.
              </p>
            </HelpSection>
          </>
        }
        actions={
          <LiveStatus
            updatedAt={updatedAt}
            refreshing={refreshing}
            onRefresh={load}
            intervalSeconds={activating ? 5 : 30}
            error={!!err}
          />
        }
      />

      <ErrorBanner err={err} />

      {a.open_alerts ? (
        <Callout tone="danger" title={`${a.open_alerts} open security ${a.open_alerts === 1 ? "alert" : "alerts"}`}>
          <Link href="/system/security-alerts" className="underline underline-offset-2">Review in Security alerts</Link>
        </Callout>
      ) : null}

      {a.activation === "waiting" && (
        <StateCard
          tone="info"
          title="Waiting for activation"
          description={<>Registered {ago(a.registered_at)}. Activate it to choose its customer, site and license.</>}
          action={can["appliances.activate"] ? <Button onClick={() => setDialog("activate")}>Activate</Button> : undefined}
        >
          {a.holds_customer_id ? (
            a.holds_customer_name ? (
              <>
                It still holds the data of <strong>{a.holds_customer_name}</strong> and can be activated only for
                that customer. For another customer, factory-reset it first.
              </>
            ) : (
              <>
                It still holds the data of a customer that is no longer in Central. Factory-reset it before
                activating it.
              </>
            )
          ) : !can["appliances.activate"] ? (
            <span className="text-muted-foreground">A platform admin can activate it.</span>
          ) : null}
        </StateCard>
      )}

      {a.activation === "activating" && (
        <StateCard
          tone="info"
          title={a.activating_on === "certificate_reissue" ? "Waiting for a new certificate" : "Activating"}
          description={
            <>
              {a.activating_on === "certificate_reissue" ? (
                <>
                  Its certificate was revoked for reissue. The appliance requests a new one by itself the next time it
                  contacts OneGate Central, and it is signed at once because the appliance is activated.
                </>
              ) : a.activating_on === "license" ? (
                <>It holds its certificate and is waiting for a license.</>
              ) : (
                activationText.explain
              )}{" "}
              This page checks every 5 seconds.
            </>
          }
          footer={
            // Offline activation requires an imported request; otherwise the package download would only fail.
            canManage && a.activating_on !== "certificate_reissue" ? (
              a.offline_request_pending ? (
                <>
                  <span className="me-auto text-xs text-muted-foreground">
                    An offline activation request is on file. Download the package and upload it in the appliance&apos;s
                    Admin Console.
                  </span>
                  <Button variant="secondary" size="sm" loading={fileBusy} onClick={() => download("package")}>
                    {!fileBusy && <Download />} Activation package
                  </Button>
                </>
              ) : (
                <span className="text-xs text-muted-foreground">
                  No internet at the site? Download the activation request from the appliance&apos;s Admin Console and
                  import it on the Appliances page; the activation package can then be downloaded here.
                </span>
              )
            ) : undefined
          }
        />
      )}

      {a.activation === "retiring" && (
        <Callout tone="warning" title={unconfirmed ? "Retirement not confirmed" : "Retiring"}>
          {unconfirmed ? (
            <>The appliance did not confirm its retirement, so its credentials are still valid.</>
          ) : (
            <>
              Retirement is signed; waiting for the appliance to confirm
              {a.retirement?.deadline ? <> (by {formatDateTime(a.retirement.deadline)})</> : null}. Its credentials
              stay valid until it does.
            </>
          )}{" "}
          {canManage && (
            <>
              If it is lost, dead or cannot reach Central,{" "}
              <Button variant="link" className="h-auto p-0 align-baseline" onClick={() => openRetire(true)}>
                retire it now without waiting
              </Button>
              .
            </>
          )}
        </Callout>
      )}

      {a.activation === "retired" && (
        <StateCard
          title="Retired"
          description={<>{activationText.explain} Its history stays in the audit log.</>}
          action={
            canManage ? (
              <Button variant="secondary" onClick={() => setDialog("delete")}>
                <Trash2 /> Delete record
              </Button>
            ) : undefined
          }
        />
      )}

      {a.replacement?.pending && (
        <Callout tone="info" title="Marked for replacement">
          It keeps working until a new appliance is activated at the same site
          {a.replacement.deadline ? <> (expected before {formatDay(a.replacement.deadline)})</> : null}. Then this one
          starts retiring: it shows <strong>Retiring</strong> until it confirms, then <strong>Retired</strong>.
        </Callout>
      )}
      {(a.replacement?.replaced_by || a.replacement?.replaces) && (
        <p className="text-sm text-muted-foreground">
          {a.replacement.replaced_by && (
            <>Replaced by <Link href={`/appliances/${a.replacement.replaced_by}`} className={LINK}>its new appliance</Link>. </>
          )}
          {a.replacement.replaces && (
            <>Replaces <Link href={`/appliances/${a.replacement.replaces}`} className={LINK}>the appliance previously at this site</Link>.</>
          )}
        </p>
      )}

      <div className="grid gap-5 lg:grid-cols-3">
        <div className="min-w-0 space-y-5 lg:col-span-2">
          {a.activation !== "waiting" && (
            <Card>
              <CardHeader>
                <CardTitle>License</CardTitle>
                <div className="flex flex-wrap items-center gap-2">
                  <LicenseBadge value={lic.state} />
                  {canLicense && (
                    <Button variant={needsLicense ? "primary" : "secondary"} size="sm" onClick={() => setDialog("license")}>
                      {hasLicense ? "Renew or change" : "Issue license"}
                    </Button>
                  )}
                </div>
              </CardHeader>
              <CardBody>
                {hasLicense ? (
                  <DescriptionList
                    layout="stacked"
                    columns={2}
                    items={[
                      {
                        label: "Clients online at once",
                        value: <span className="tabular">{(lic.max_concurrent_online_guests ?? current?.max_concurrent_online_guests)?.toLocaleString() ?? "—"}</span>,
                      },
                      {
                        label: "Valid until",
                        value: formatLicenseDay(lic.valid_until),
                        hint: lic.valid_until ? daysLeftWords(lic.valid_until) : undefined,
                      },
                      {
                        label: "Grace period",
                        value: current?.grace_period_days != null ? `${current.grace_period_days} days` : "—",
                        hint: lic.grace_ends_at ? `ends ${formatLicenseDay(lic.grace_ends_at)}` : undefined,
                      },
                      { label: "Modules", value: modulesSummary(lic.modules ?? current?.modules) },
                    ]}
                  />
                ) : (
                  <p className="text-sm text-muted-foreground">
                    {licenseInfo("none").explain}
                    {a.activation === "activated" && " Clients cannot get online until it has one."}
                  </p>
                )}
              </CardBody>
              {canLicense && hasLicense && lic.id && (
                <CardFooter>
                  {(lic.state === "active" || lic.state === "expiring") && (
                    <Button variant="ghost" size="sm" onClick={() => openReason(licenseAction("suspend"))}>
                      <Pause /> Suspend
                    </Button>
                  )}
                  {lic.state === "suspended" && (
                    <Button variant="secondary" size="sm" onClick={() => openReason(licenseAction("resume"))}>
                      <Play /> Resume
                    </Button>
                  )}
                  {lic.state !== "revoked" && (
                    <Button variant="ghost" size="sm" className={DANGER_GHOST} onClick={() => openReason(licenseAction("revoke"))}>
                      <Ban /> Revoke
                    </Button>
                  )}
                  {lic.state !== "revoked" && (
                    <Button variant="ghost" size="sm" className="ms-auto" loading={fileBusy} onClick={() => download("license")}>
                      {!fileBusy && <Download />} Offline license file
                    </Button>
                  )}
                </CardFooter>
              )}
            </Card>
          )}

          {versions.length > 0 && (
            <SectionCard
              title="License versions"
              description="Every license issued to this appliance, newest first."
              flush
            >
              <HistoryList
                label="License versions"
                items={versions.map((l): HistoryItem => ({
                  id: l.id,
                  title: `Version ${l.license_version ?? "—"}`,
                  badge: <LicenseBadge value={l.state} />,
                  summary: [
                    l.max_concurrent_online_guests != null ? `${l.max_concurrent_online_guests.toLocaleString()} clients` : null,
                    `until ${formatLicenseDay(l.valid_until)}`,
                    modulesSummary(l.modules),
                  ].filter(Boolean).join(" · "),
                  at: l.issued_at ? <span title={formatDateTime(l.issued_at)}>Issued {formatDay(l.issued_at)}</span> : undefined,
                  tone: licenseTone(l.state),
                }))}
                selectedId={version?.id}
                onSelect={(it) => setVersion(versions.find((l) => l.id === it.id) ?? null)}
              />
            </SectionCard>
          )}

          <SectionCard
            title="Activity"
            flush
            actions={
              can["system.read"] ? (
                <Link href={`/system/audit?appliance_id=${a.id}`} className={cn("text-sm", LINK)}>
                  Full audit log
                </Link>
              ) : undefined
            }
          >
            <HistoryList
              label="Recent activity"
              emptyLabel="No activity yet."
              items={events.map((e, i): HistoryItem => ({
                id: String(i),
                title: (
                  <>
                    {actionWords(e.action)}
                    {e.count > 1 && <span className="ms-1.5 font-normal text-muted-foreground">×{e.count}</span>}
                  </>
                ),
                actor: e.actor_email ?? e.actor ?? undefined,
                at: <time dateTime={e.ts ?? e.at} title={formatDateTime(e.ts ?? e.at)}>{ago(e.ts ?? e.at)}</time>,
                summary: e.reason ?? e.detail ?? undefined,
                tone: historyTone(actionTone(e.action)),
              }))}
              onSelect={(it) => setEvent(events[Number(it.id)] ?? null)}
            />
          </SectionCard>
        </div>

        <div className="min-w-0 space-y-5">
          <SectionCard
            title="Installed at"
            actions={
              canManage && a.activation === "activated" ? (
                <Button variant="ghost" size="sm" onClick={() => setDialog("move")}>
                  <ArrowRightLeft /> Move
                </Button>
              ) : undefined
            }
          >
            <DescriptionList
              items={[
                {
                  label: "Customer",
                  value: a.customer_id ? (
                    <Link href={`/customers/${a.customer_id}`} className={LINK}>{a.customer_name ?? "Customer"}</Link>
                  ) : "Not assigned yet",
                },
                { label: "Site", value: a.site_name ?? null, hint: a.site_name ? siteTypeLabel(a.site_type) : undefined },
              ]}
            />
          </SectionCard>

          <Card>
            <CardHeader><CardTitle>Appliance</CardTitle></CardHeader>
            <CardBody className="py-2">
              <DescriptionList
                items={[
                  { label: "Connection", value: connectionSentence(a) },
                  { label: "Registered", value: formatDateTime(a.registered_at) },
                  { label: "Activated", value: formatDateTime(a.activated_at), hidden: !a.activated_at },
                  { label: "Last address", value: <span className="font-mono text-xs">{a.last_public_ip}</span>, hidden: !a.last_public_ip },
                  { label: "Software version", value: a.version, hidden: !a.version },
                ]}
              />
            </CardBody>
            {canManage && (a.activation === "activated" || a.activation === "activating" || a.activation === "waiting") && (
              <CardFooter>
                {a.activation === "activated" || a.activation === "activating" ? (
                  <Button variant="ghost" size="sm" className={DANGER_GHOST} onClick={() => openRetire(false)}>
                    <Power /> Retire appliance
                  </Button>
                ) : (
                  <Button variant="ghost" size="sm" className={DANGER_GHOST} onClick={() => setDialog("delete")}>
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
            onAction={openReason}
          />
        </div>
      </div>

      <DetailsDrawer
        open={!!version}
        onOpenChange={(v) => { if (!v) setVersion(null); }}
        eyebrow="License version"
        title={`Version ${version?.license_version ?? "—"}`}
        description={a.serial}
        badges={version ? (
          <>
            <LicenseBadge value={version.state} />
            {version.id === lic.id && <Badge tone="accent">Current</Badge>}
          </>
        ) : undefined}
      >
        {version && (
          <DetailsSection title="Terms">
            <DescriptionList
              items={[
                { label: "Clients online at once", value: version.max_concurrent_online_guests?.toLocaleString() ?? null },
                { label: "Valid from", value: formatLicenseDay(version.valid_from), hidden: !version.valid_from },
                { label: "Valid until", value: formatLicenseDay(version.valid_until) },
                { label: "Grace period", value: version.grace_period_days != null ? `${version.grace_period_days} days` : null },
                { label: "Grace ends", value: formatLicenseDay(version.grace_ends_at), hidden: !version.grace_ends_at },
                { label: "Modules", value: modulesSummary(version.modules) },
                { label: "Issued", value: formatDateTime(version.issued_at) },
              ]}
            />
          </DetailsSection>
        )}
      </DetailsDrawer>

      <DetailsDrawer
        open={!!event}
        onOpenChange={(v) => { if (!v) setEvent(null); }}
        eyebrow="Activity"
        title={event ? actionWords(event.action) : ""}
        description={a.serial}
      >
        {event && (
          <>
            <DetailsSection title="Event">
              <DescriptionList
                items={[
                  { label: "When", value: formatDateTime(event.ts ?? event.at) },
                  { label: "Repeated", value: `${event.count} times, first ${formatDateTime(event.first_at)}`, hidden: event.count < 2 },
                  { label: "By", value: event.actor_email ?? event.actor ?? null },
                  { label: "Reason", value: event.reason ?? null, hidden: !event.reason },
                  { label: "Detail", value: event.detail ?? null, hidden: !event.detail },
                  { label: "Action", value: <span className="font-mono text-xs">{event.action}</span> },
                ]}
              />
            </DetailsSection>
            {hasPayload(event.payload) && (
              <DetailsSection title="Recorded details">
                <PayloadView payload={event.payload} />
              </DetailsSection>
            )}
          </>
        )}
      </DetailsDrawer>

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
        title={a.activation === "retiring" ? `Retire ${a.serial} now` : `Retire ${a.serial}`}
        description="Takes the appliance out of service for good. Either way it ends as Retired."
        confirmLabel={emergency ? "Retire now" : "Retire appliance"}
        confirmVariant="danger"
        busy={actionBusy}
        error={actionErr}
        consequences={[
          emergency
            ? "Its license and credentials are revoked now, without waiting for the appliance to confirm."
            : "Central signs its retirement; the appliance confirms on its next contact, normally within a minute. It shows Retiring until then, and its credentials stay valid until it confirms.",
          "It stops serving clients and can no longer connect to Central.",
          "It cannot be undone. Only its record can then be deleted.",
        ]}
        confirmText={a.serial}
        confirmTextLabel="Type the serial"
        requireReason
        onConfirm={retire}
      >
        {a.activation !== "retiring" && (
          <label className="flex items-start gap-3 text-sm">
            <Switch checked={emergency} onCheckedChange={setEmergency} label="Emergency retire" />
            <span>
              <span className="font-medium">Emergency: don&apos;t wait for the appliance</span>
              <span className="block text-xs text-muted-foreground">For a lost, stolen or dead appliance.</span>
            </span>
          </label>
        )}
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

/** The lifecycle notice at the top of the page: a titled card, tinted for a state in progress. */
function StateCard({
  title, description, action, tone, footer, children,
}: {
  title: string;
  description: React.ReactNode;
  action?: React.ReactNode;
  tone?: "info";
  footer?: React.ReactNode;
  children?: React.ReactNode;
}) {
  return (
    <Card className={cn(tone === "info" && "border-info/40")}>
      <CardHeader className={cn(!children && !footer && "border-b-0")}>
        <div className="min-w-0">
          <CardTitle>{title}</CardTitle>
          <CardDescription>{description}</CardDescription>
        </div>
        {action}
      </CardHeader>
      {children && <CardBody className="text-sm">{children}</CardBody>}
      {footer && <CardFooter>{footer}</CardFooter>}
    </Card>
  );
}

function historyTone(t: Tone): HistoryItem["tone"] {
  return t === "default" ? "neutral" : t;
}

function licenseTone(state: string): HistoryItem["tone"] {
  return historyTone(licenseInfo(state).tone);
}

function daysLeftWords(until: string): string {
  const days = Math.ceil((Date.parse(until) - Date.now()) / 86_400_000);
  if (Number.isNaN(days)) return "";
  if (days > 1) return `${days.toLocaleString()} days left`;
  if (days === 1) return "1 day left";
  if (days === 0) return "ends today";
  return days === -1 ? "ended yesterday" : `ended ${(-days).toLocaleString()} days ago`;
}

// Collapse consecutive retries so they do not bury other events.
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
  const mono = (v?: string | null) => (v ? <span className="break-all font-mono text-xs">{v}</span> : null);
  return (
    <details className="group rounded-lg border border-border bg-card shadow-card">
      <summary
        className={cn(
          "flex cursor-pointer list-none items-center justify-between gap-3 rounded-lg px-5 py-3.5 text-emphasis",
          "hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden",
        )}
      >
        Advanced
        <ChevronDown className="size-4 text-muted-foreground transition-transform group-open:rotate-180 motion-reduce:transition-none" aria-hidden />
      </summary>
      <div className="space-y-5 border-t border-border px-5 py-4">
        {canManage && live && (
          <section className="space-y-1.5">
            <h3 className="text-label">Repair</h3>
            <div className="-mx-2 flex flex-col items-start">
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
                  description: "Use it when new hardware takes this appliance's place. This one keeps working until the new appliance is activated at the same site; then it starts retiring and is retired once it confirms.",
                  confirmLabel: "Mark for replacement",
                  url: `/cloud/v1/appliances/${a.id}/replace`,
                  done: "Marked for replacement",
                })}>
                  <ArrowRightLeft /> Mark for replacement
                </Button>
              )}
              {a.offline_request_pending && (
                <Button variant="ghost" size="sm" disabled={fileBusy} onClick={onPackage}>
                  <Download /> Offline activation package
                </Button>
              )}
            </div>
          </section>
        )}

        <section className="space-y-1.5">
          <h3 className="text-label">Identity and certificate</h3>
          <DescriptionList
            className="text-xs"
            items={[
              { label: "Appliance ID", value: mono(a.id) },
              { label: "WAN MAC", value: mono(a.identity?.wan_mac) },
              { label: "LAN MAC", value: mono(a.identity?.lan_mac) },
              { label: "Hardware fingerprint", value: mono(a.identity?.hardware_fingerprint) },
              { label: "Identity key fingerprint", value: mono(a.identity?.identity_key_fingerprint) },
              { label: "Certificate fingerprint", value: mono(a.identity?.cert_fingerprint) },
              {
                label: "Certificate expires",
                value: a.identity?.cert_not_after
                  ? `${formatDay(a.identity.cert_not_after)}${certDays !== null ? ` (${certDays} days)` : ""}`
                  : null,
              },
            ]}
          />
        </section>

        <section className="space-y-1.5">
          <h3 className="text-label">Assignment and license</h3>
          <DescriptionList
            className="text-xs"
            items={[
              {
                label: "Assignment version",
                value: a.assignment?.version != null
                  ? `v${a.assignment.version}${a.assignment.acked_version != null ? ` · appliance confirmed v${a.assignment.acked_version}` : ""}`
                  : null,
              },
              { label: "Assignment signing key", value: mono(a.assignment?.signer_key_id) },
              { label: "License version", value: a.license?.license_version != null ? `v${a.license.license_version}` : null },
            ]}
          />
        </section>
      </div>
    </details>
  );
}
