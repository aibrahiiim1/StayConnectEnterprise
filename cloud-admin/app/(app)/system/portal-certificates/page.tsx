"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { GlobeLock, ShieldX } from "lucide-react";
import {
  api, withStepUp,
  type PortalCert, type PortalFleetRow, type PortalTLSEvent, type PortalTLSSettings, type PortalTLSTestResult,
} from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Field, FieldGrid, Input, Select } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { DescriptionList, SearchInput } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { HistoryList } from "@/components/ui/history";
import { RowActions } from "@/components/ui/row-actions";
import { MonoId, Skeleton, SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { StateBadge } from "@/components/status-badge";
import { ListState, PayloadView, hasPayload } from "@/components/record-view";
import { HelpList, HelpSection } from "@/components/help";
import { usePermissions } from "@/lib/permissions";
import {
  ago, daysUntil, formatDateTime, formatDay, portalCertInfo, portalDNSInfo, portalDomainInfo, sentenceCase,
} from "@/lib/status";

// Appliance private keys stay local; certificate authority changes require password confirmation.
// The DNS token is write-only, and Central only reports whether one is set.

const DIRECTORY_WORDS: Record<string, string> = { staging: "Staging", production: "Production", other: "Test CA" };

function dirBadge(c: PortalCert) {
  if (!c.acme_directory) return null;
  const tone = c.acme_directory === "production" ? "ok" : c.acme_directory === "staging" ? "warn" : "info";
  return <Badge tone={tone}>{DIRECTORY_WORDS[c.acme_directory] ?? sentenceCase(c.acme_directory)}</Badge>;
}

function mainCert(r: PortalFleetRow): PortalCert | undefined {
  return r.certificates.find((c) => c.kind === "ONEGATE") ?? r.certificates[0];
}

export default function PortalCertificatesPage() {
  const toast = useToast();
  const { can } = usePermissions();
  const canManage = can["portalTls.manage"];

  const [settings, setSettings] = useState<PortalTLSSettings | null>(null);
  const [fleet, setFleet] = useState<PortalFleetRow[] | null>(null);
  const [err, setErr] = useState<unknown>(null);

  const [draft, setDraft] = useState({ acme_directory: "staging", contact_email: "", zone_name: "", weekly_new_limit: "45" });
  const [savingSettings, setSavingSettings] = useState(false);
  const [settingsErr, setSettingsErr] = useState<unknown>(null);

  // Clear the write-only DNS token after sending it.
  const [token, setToken] = useState("");
  const [savingToken, setSavingToken] = useState(false);
  const [tokenErr, setTokenErr] = useState<unknown>(null);

  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<PortalTLSTestResult | null>(null);
  const [applyingCAA, setApplyingCAA] = useState(false);
  const [confirmCAA, setConfirmCAA] = useState(false);
  const [confirmProduction, setConfirmProduction] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);

  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<PortalFleetRow | null>(null);
  const [events, setEvents] = useState<PortalTLSEvent[] | null>(null);
  const [eventsErr, setEventsErr] = useState<unknown>(null);
  const [eventId, setEventId] = useState<string | null>(null);
  const [revokeFor, setRevokeFor] = useState<PortalFleetRow | null>(null);
  const [revoking, setRevoking] = useState(false);
  const [revokeErr, setRevokeErr] = useState<unknown>(null);

  const adopt = useCallback((s: PortalTLSSettings) => {
    setSettings(s);
    setDraft({
      acme_directory: s.acme_directory,
      contact_email: s.contact_email ?? "",
      zone_name: s.zone_name ?? "",
      weekly_new_limit: String(s.weekly_new_limit ?? 45),
    });
  }, []);

  const load = useCallback(async () => {
    try {
      const [s, f] = await Promise.all([
        api.get<PortalTLSSettings>("/cloud/v1/portal-tls/settings"),
        api.get<{ items: PortalFleetRow[] }>("/cloud/v1/portal-tls/fleet"),
      ]);
      adopt(s);
      setFleet(f.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e);
      setFleet((p) => p ?? []);
    }
  }, [adopt]);
  useEffect(() => { void load(); }, [load]);

  const dirty = settings
    ? draft.acme_directory !== settings.acme_directory ||
      draft.contact_email.trim() !== (settings.contact_email ?? "") ||
      draft.zone_name.trim() !== (settings.zone_name ?? "") ||
      Number(draft.weekly_new_limit) !== settings.weekly_new_limit
    : false;

  async function saveSettings() {
    if (!settings) return;
    setSavingSettings(true);
    setSettingsErr(null);
    try {
      const s = await withStepUp(() =>
        api.put<PortalTLSSettings>("/cloud/v1/portal-tls/settings", {
          acme_directory: draft.acme_directory,
          contact_email: draft.contact_email.trim(),
          zone_name: draft.zone_name.trim(),
          weekly_new_limit: Number(draft.weekly_new_limit),
        }),
      );
      adopt(s);
      toast.success("Portal certificate settings saved");
    } catch (e) {
      setSettingsErr(e);
    } finally {
      setSavingSettings(false);
    }
  }

  function onSaveSettings() {
    // Production uses a browser-trusted CA with real rate limits, so switching requires confirmation.
    if (settings && draft.acme_directory === "production" && settings.acme_directory !== "production") {
      setConfirmProduction(true);
      return;
    }
    void saveSettings();
  }

  async function saveToken() {
    setSavingToken(true);
    setTokenErr(null);
    try {
      await withStepUp(() => api.put<{ set: boolean }>("/cloud/v1/portal-tls/dns-token", { token: token.trim() }));
      setToken("");
      toast.success("DNS token stored", "Run Test DNS access to confirm it reaches the zone.");
      await load();
    } catch (e) {
      setTokenErr(e);
    } finally {
      setSavingToken(false);
    }
  }

  async function testAccess() {
    setTesting(true);
    setActionErr(null);
    try {
      const r = await withStepUp(() => api.post<PortalTLSTestResult>("/cloud/v1/portal-tls/test"));
      setTestResult(r);
      toast.success("DNS access and ACME account confirmed", r.zone_name);
      await load();
    } catch (e) {
      setActionErr(e);
    } finally {
      setTesting(false);
    }
  }

  async function applyCAA() {
    setApplyingCAA(true);
    setActionErr(null);
    try {
      const r = await withStepUp(() =>
        api.post<{ issue: string; other_issuers: string[] }>("/cloud/v1/portal-tls/caa"),
      );
      setConfirmCAA(false);
      toast.success(
        "CAA applied",
        r.other_issuers?.length ? `Other CAs still allowed: ${r.other_issuers.join(", ")}` : "Only this ACME account may issue.",
      );
      await load();
    } catch (e) {
      setActionErr(e);
      setConfirmCAA(false);
    } finally {
      setApplyingCAA(false);
    }
  }

  async function openDetails(r: PortalFleetRow) {
    setSelected(r);
    setEventId(null);
    setEvents(null);
    setEventsErr(null);
    try {
      const res = await api.get<{ items: PortalTLSEvent[] }>(
        `/cloud/v1/portal-tls/events?appliance_id=${encodeURIComponent(r.appliance_id)}`,
      );
      setEvents(res.items ?? []);
    } catch (e) {
      setEventsErr(e);
      setEvents([]);
    }
  }

  async function onRevoke({ reason }: { reason: string }) {
    const r = revokeFor;
    if (!r) return;
    setRevoking(true);
    setRevokeErr(null);
    try {
      await withStepUp(() =>
        api.post(`/cloud/v1/portal-tls/appliances/${encodeURIComponent(r.appliance_id)}/revoke`, { reason }),
      );
      setRevokeFor(null);
      toast.success("Revocation queued", `${r.hostname} orders a new certificate on its next sync.`);
      await load();
    } catch (e) {
      setRevokeErr(e);
    } finally {
      setRevoking(false);
    }
  }

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (fleet ?? []).filter(
      (r) =>
        !q ||
        [r.serial, r.hostname, r.custom_domain, r.customer_name, r.site_name, r.portal_ip].join(" ").toLowerCase().includes(q),
    );
  }, [fleet, query]);

  const askRevoke = (r: PortalFleetRow) => {
    setSelected(null);
    setRevokeErr(null);
    setRevokeFor(r);
  };
  const openEvent = events?.find((e) => String(e.id) === eventId) ?? null;

  const revocable = (r: PortalFleetRow) => r.certificates.some((c) => (c.status === "ISSUED" || c.status === "SUPERSEDED") && !c.revoked);

  return (
    <PageShell width="wide">
      <PageHeader
        title="Portal certificates"
        description="Every appliance's portal hostname, DNS record and HTTPS certificates."
        help={
          <>
            <HelpSection title="How it works">
              <p>
                Each appliance generates its own key and certificate request; Central only answers the CA&apos;s DNS
                challenge and publishes the hostname. Private keys never leave the appliance.
              </p>
            </HelpSection>
            <HelpSection title="Setting up">
              <HelpList
                items={[
                  <>Set the <strong>contact email</strong> and keep the directory on <strong>Staging</strong>.</>,
                  <>Paste a Cloudflare API token scoped to the zone (<strong>Zone: Read</strong>, <strong>DNS: Edit</strong>), then <strong>Test DNS access</strong>.</>,
                  <>Once one appliance has a staging certificate, switch to <strong>Production</strong> and <strong>Apply CAA</strong>.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Limits">
              <p>
                The weekly limit caps <strong>new</strong> hostnames under the zone (Let&apos;s Encrypt allows 50 per week). Renewals and
                hotel domains do not count; orders over the limit wait in the queue.
              </p>
            </HelpSection>
            <HelpSection title="Revoking">
              <p>
                Revoking makes the appliance order a fresh certificate with a new key on its next sync. Retiring or deleting an
                appliance revokes its certificates and removes its DNS record by itself.
              </p>
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} />

      {settings && settings.test_overrides.length > 0 && (
        <Callout tone="warning" title="Test overrides are active on this Central">
          Certificates come from a test CA and are trusted by nobody: <span className="font-mono text-xs">{settings.test_overrides.join(" · ")}</span>
        </Callout>
      )}
      {settings && !settings.available && (
        <Callout tone="info" title="Not issuing yet">
          {settings.unavailable_reason ? `${sentenceCase(settings.unavailable_reason)}.` : null} Appliances keep serving their portal over HTTP until this is done.
        </Callout>
      )}

      {!settings && !err ? (
        <div className="grid gap-5 lg:grid-cols-2" aria-busy="true">
          <span className="sr-only">Loading</span>
          <Skeleton className="h-72" /><Skeleton className="h-72" />
        </div>
      ) : settings ? (
        <div className="grid gap-5 lg:grid-cols-2">
          <SectionCard
            title="ACME and DNS"
            description="Which CA issues, who it writes to, and the zone Central publishes in."
            footer={
              canManage ? (
                <Button disabled={!dirty} loading={savingSettings} onClick={onSaveSettings}>Save settings</Button>
              ) : undefined
            }
            bodyClassName="space-y-4"
          >
            <ErrorBanner err={settingsErr} />
            <Field
              label="ACME directory"
              hint={
                draft.acme_directory === "production"
                  ? "Trusted by browsers; counts against Let's Encrypt's real limits."
                  : "Not trusted by browsers. Prove one issuance here first."
              }
            >
              <Select
                value={draft.acme_directory}
                disabled={!canManage}
                onChange={(e) => setDraft((d) => ({ ...d, acme_directory: e.target.value }))}
              >
                <option value="staging">Staging (Let&apos;s Encrypt test)</option>
                <option value="production">Production</option>
              </Select>
            </Field>
            {draft.acme_directory === "production" && settings.acme_directory !== "production" && (
              <Callout tone="warning">Switch only after a staging certificate has been issued and installed on an appliance.</Callout>
            )}
            <Field label="Contact email" hint="Let's Encrypt writes here about expiry and incidents." required>
              <Input
                type="email"
                value={draft.contact_email}
                disabled={!canManage}
                onChange={(e) => setDraft((d) => ({ ...d, contact_email: e.target.value }))}
                placeholder="pki@onegate.work"
              />
            </Field>
            <FieldGrid cols={2}>
              <Field label="DNS zone">
                <Input
                  value={draft.zone_name}
                  disabled={!canManage}
                  onChange={(e) => setDraft((d) => ({ ...d, zone_name: e.target.value }))}
                />
              </Field>
              <Field label="New hostnames per week" hint="1–50">
                <Input
                  type="number"
                  min={1}
                  max={50}
                  value={draft.weekly_new_limit}
                  disabled={!canManage}
                  onChange={(e) => setDraft((d) => ({ ...d, weekly_new_limit: e.target.value }))}
                />
              </Field>
            </FieldGrid>
            <DescriptionList
              items={[
                { label: "Directory URL", value: <span className="break-all font-mono text-xs">{settings.directory_url}</span> },
                { label: "ACME account", value: settings.account_url ? <MonoId value={settings.account_url} head={28} title="Account" /> : "Not registered yet" },
                { label: "Zone id", value: settings.zone_id ? <MonoId value={settings.zone_id} head={12} title="Zone id" /> : "Not verified yet" },
                { label: "CAA", value: settings.caa_applied_at ? `Applied ${formatDateTime(settings.caa_applied_at)}` : "Not applied" },
              ]}
            />
          </SectionCard>

          <SectionCard
            title="DNS API token"
            description="Kept on Central only, in a root-only file. It is never shown again."
            actions={<StatusBadge tone={settings.token_set ? "ok" : "warn"}>{settings.token_set ? "Set" : "Not set"}</StatusBadge>}
            footer={
              canManage ? (
                <>
                  <Button variant="ghost" disabled={applyingCAA || !settings.available} onClick={() => { setActionErr(null); setConfirmCAA(true); }}>
                    Apply CAA
                  </Button>
                  <Button variant="secondary" loading={testing} onClick={testAccess}>Test DNS access</Button>
                </>
              ) : undefined
            }
            bodyClassName="space-y-4"
          >
            <ErrorBanner err={tokenErr} />
            {settings.dns_provider !== "cloudflare" && (
              <Callout tone="info">DNS provider <span className="font-mono">{settings.dns_provider}</span> needs no token.</Callout>
            )}
            {canManage && (
              <form
                className="flex flex-wrap items-end gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (token.trim()) void saveToken();
                }}
              >
                <Field label={settings.token_set ? "Replace token" : "Cloudflare API token"} className="min-w-0 flex-1">
                  <Input
                    type="password"
                    autoComplete="off"
                    spellCheck={false}
                    data-1p-ignore
                    data-lpignore="true"
                    value={token}
                    onChange={(e) => setToken(e.target.value)}
                    placeholder="Zone-scoped token: Zone Read, DNS Edit"
                  />
                </Field>
                <Button type="submit" variant="secondary" disabled={!token.trim()} loading={savingToken}>
                  Store token
                </Button>
              </form>
            )}
            <ErrorBanner err={actionErr} />
            {testResult && (
              <div className="rounded-md border border-border bg-surface/50 px-3 py-1">
                <DescriptionList
                  items={[
                    { label: "Zone", value: testResult.zone_name },
                    { label: "Nameservers", value: testResult.nameservers.length ? testResult.nameservers.join(", ") : null },
                    { label: "ACME account", value: <MonoId value={testResult.account_url} head={28} title="Account" /> },
                    { label: "Directory", value: <span className="break-all font-mono text-xs">{testResult.directory}</span> },
                  ]}
                />
              </div>
            )}
          </SectionCard>
        </div>
      ) : null}

      <SectionCard title="Appliances" description="One row per appliance with a portal hostname." flush>
        <FilterBar
          inset
          search={<SearchInput value={query} onChange={setQuery} placeholder="Search serial, hostname, domain" label="Search portal hostnames" />}
        />
        <ListState
          rows={fleet === null ? null : visible}
          cols={6}
          filtered={!!query}
          noMatch={<EmptyState size="sm" title="No appliances match" />}
          empty={<EmptyState size="sm" icon={<GlobeLock />} title="No portal hostnames yet" hint="An activated appliance receives its hostname the first time it syncs." />}
        >
          <Table aria-label="Portal certificates">
            <THead>
              <TR>
                <TH>Appliance</TH>
                <TH>Hostname</TH>
                <TH className="hidden md:table-cell">DNS</TH>
                <TH className="hidden lg:table-cell">Hotel domain</TH>
                <TH>Certificate</TH>
                <TH className="hidden xl:table-cell">Last error</TH>
                <TH className="w-0"><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {visible.map((r) => {
                const c = mainCert(r);
                const left = daysUntil(c?.not_after);
                return (
                  <TR key={r.appliance_id} onClick={() => void openDetails(r)} selected={selected?.appliance_id === r.appliance_id}>
                    <TD nowrap>
                      <Link
                        href={`/appliances/${r.appliance_id}`}
                        onClick={(e) => e.stopPropagation()}
                        className="rounded font-mono text-xs underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        {r.serial}
                      </Link>
                      <div className="max-w-[12rem] truncate text-xs text-muted-foreground">
                        {[r.customer_name, r.site_name].filter(Boolean).join(" · ") || "—"}
                      </div>
                    </TD>
                    <TD>
                      <div className="break-all font-mono text-xs">{r.hostname}</div>
                      <div className="text-xs text-muted-foreground">{r.portal_ip ?? "no address yet"}</div>
                    </TD>
                    <TD nowrap className="hidden md:table-cell"><StateBadge info={portalDNSInfo(r.dns_state)} /></TD>
                    <TD className="hidden lg:table-cell">
                      {r.custom_domain ? (
                        <>
                          <div className="break-all font-mono text-xs">{r.custom_domain}</div>
                          <StateBadge info={portalDomainInfo(r.custom_domain_status)} className="mt-1" />
                        </>
                      ) : <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD nowrap>
                      {c ? (
                        <>
                          <div className="flex items-center gap-1.5">
                            <StateBadge info={portalCertInfo(c.status, c.revoked)} />
                            {dirBadge(c)}
                          </div>
                          {c.not_after && (
                            <div className="mt-0.5 text-xs text-muted-foreground">
                              Expires {formatDay(c.not_after)}
                              {left !== null && c.status === "ISSUED" ? ` (${left < 0 ? "expired" : `${left} days`})` : ""}
                            </div>
                          )}
                          {r.certificates.length > 1 && (
                            <div className="text-xs text-muted-foreground">+ {r.certificates.length - 1} more</div>
                          )}
                        </>
                      ) : <span className="text-muted-foreground">None yet</span>}
                    </TD>
                    <TD className="hidden max-w-xs xl:table-cell">
                      {r.last_error ? <span className="line-clamp-2 text-xs text-destructive" title={r.last_error}>{r.last_error}</span> : <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD className="w-0">
                      <RowActions
                        subject={r.serial}
                        items={[
                          { label: "Certificates and events", onSelect: () => void openDetails(r) },
                          { label: "Revoke certificates", danger: true, hidden: !canManage || !revocable(r), onSelect: () => askRevoke(r) },
                        ]}
                      />
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        </ListState>
      </SectionCard>

      <DetailsDrawer
        open={!!selected}
        onOpenChange={(v) => { if (!v) setSelected(null); }}
        width="lg"
        eyebrow="Portal certificates"
        title={selected ? <span className="font-mono">{selected.serial}</span> : ""}
        description={selected?.hostname}
        badges={selected ? <StateBadge info={portalDNSInfo(selected.dns_state)} /> : undefined}
        actions={
          selected && canManage && revocable(selected) ? (
            <Button variant="danger-outline" size="sm" onClick={() => askRevoke(selected)}>Revoke</Button>
          ) : undefined
        }
      >
        {selected && (
          <>
            <DetailsSection title="Hostname and DNS">
              <DescriptionList
                items={[
                  { label: "Customer · site", value: [selected.customer_name, selected.site_name].filter(Boolean).join(" · ") || null },
                  { label: "Hostname", value: <span className="break-all font-mono text-xs">{selected.hostname}</span> },
                  { label: "Portal address", value: selected.portal_ip ? <span className="font-mono text-xs">{selected.portal_ip}</span> : null },
                  { label: "DNS error", value: selected.dns_error ?? null, hidden: !selected.dns_error },
                  {
                    label: "Hotel domain",
                    value: selected.custom_domain ? (
                      <span className="inline-flex flex-wrap items-center gap-2">
                        <span className="break-all font-mono text-xs">{selected.custom_domain}</span>
                        <StateBadge info={portalDomainInfo(selected.custom_domain_status)} />
                      </span>
                    ) : null,
                  },
                  { label: "Hotel domain error", value: selected.custom_domain_error ?? null, hidden: !selected.custom_domain_error },
                  { label: "Last error", value: selected.last_error ? <span className="text-destructive">{selected.last_error}</span> : null, hidden: !selected.last_error },
                ]}
              />
            </DetailsSection>

            <DetailsSection title="Certificate orders">
              {selected.certificates.length === 0 ? (
                <p className="text-sm text-muted-foreground">No certificate ordered yet.</p>
              ) : (
                <div className="divide-y divide-border rounded-md border border-border">
                  {selected.certificates.map((c) => (
                    <div key={c.order_id} className="space-y-2 px-3 py-2.5">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="text-sm font-medium">{c.kind === "CUSTOM" ? "Hotel domain" : "OneGate hostname"}</span>
                        <StateBadge info={portalCertInfo(c.status, c.revoked)} />
                        {dirBadge(c)}
                      </div>
                      <DescriptionList
                        className="text-xs"
                        items={[
                          { label: "Hostname", value: <span className="break-all font-mono">{c.hostname}</span> },
                          { label: "Expires", value: formatDay(c.not_after), hidden: !c.not_after },
                          { label: "Renews after", value: formatDay(c.renew_after), hidden: !c.renew_after },
                          { label: "Serial", value: c.serial ? <MonoId value={c.serial} head={16} title="Serial" /> : null, hidden: !c.serial },
                          { label: "Order", value: <MonoId value={c.order_id} head={12} title="Order" /> },
                          { label: "Error", value: c.error ? <span className="text-destructive">{c.error}</span> : null, hidden: !c.error },
                        ]}
                      />
                    </div>
                  ))}
                </div>
              )}
            </DetailsSection>

            <DetailsSection title="Events" description="Newest first.">
              <ErrorBanner err={eventsErr} />
              {events === null ? (
                <SkeletonRows rows={4} cols={2} />
              ) : (
                <div className="-mx-1 rounded-md border border-border">
                  <HistoryList
                    label="Portal certificate events"
                    emptyLabel="No events."
                    items={events.map((e) => ({
                      id: String(e.id),
                      title: sentenceCase(e.kind.toLowerCase()),
                      actor: e.actor ?? undefined,
                      at: <time dateTime={e.at} title={formatDateTime(e.at)}>{ago(e.at)}</time>,
                      summary: e.hostname ?? undefined,
                    }))}
                    selectedId={eventId}
                    onSelect={(it) => setEventId(it.id === eventId ? null : it.id)}
                  />
                </div>
              )}
              {openEvent && hasPayload(openEvent.detail) && (
                <div className="rounded-md border border-border bg-surface/50 px-3 py-1">
                  <PayloadView payload={openEvent.detail} />
                </div>
              )}
            </DetailsSection>
          </>
        )}
      </DetailsDrawer>

      <ConfirmDialog
        open={!!revokeFor}
        onOpenChange={(v) => { if (!v) setRevokeFor(null); }}
        title="Revoke portal certificates"
        description={revokeFor ? <>Every valid certificate of <span className="font-mono">{revokeFor.serial}</span>.</> : undefined}
        confirmLabel="Revoke"
        confirmVariant="danger"
        busy={revoking}
        error={revokeErr}
        requireReason
        reasonPlaceholder="Why are these certificates being revoked?"
        consequences={[
          "The CA marks the certificates revoked; browsers that check will refuse them.",
          "The appliance orders a new certificate with a new key on its next sync, and serves HTTP until it arrives.",
        ]}
        onConfirm={onRevoke}
      />

      <ConfirmDialog
        open={confirmCAA}
        onOpenChange={setConfirmCAA}
        title="Apply CAA to the zone"
        description={settings ? <>Pins certificate issuance for <span className="font-mono">{settings.zone_name}</span> to Let&apos;s Encrypt and this ACME account.</> : undefined}
        confirmLabel="Apply CAA"
        busy={applyingCAA}
        consequences={[
          "Only this ACME account can obtain certificates for names in the zone from Let's Encrypt.",
          "Wildcard certificates are forbidden for the whole zone.",
          "Switching between staging and production registers a new account: apply CAA again afterwards.",
        ]}
        onConfirm={applyCAA}
      />

      <ConfirmDialog
        open={confirmProduction}
        onOpenChange={setConfirmProduction}
        title="Switch to the production CA"
        description="Production certificates are publicly trusted and subject to Let's Encrypt's real rate limits."
        confirmLabel="Switch to production"
        onConfirm={async () => {
          setConfirmProduction(false);
          await saveSettings();
        }}
      >
        <Callout tone="warning" icon={<ShieldX />}>
          Do this only after a staging certificate was issued and installed on an appliance.
        </Callout>
      </ConfirmDialog>
    </PageShell>
  );
}
