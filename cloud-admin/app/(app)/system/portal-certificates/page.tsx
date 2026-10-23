"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { GlobeLock, History, ShieldX } from "lucide-react";
import {
  api, withStepUp,
  type PortalCert, type PortalFleetRow, type PortalTLSEvent, type PortalTLSSettings, type PortalTLSTestResult,
} from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { SearchInput } from "@/components/ui/data";
import { MonoId, Skeleton, SkeletonRows } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetHeader } from "@/components/ui/sheet";
import { useToast } from "@/components/ui/toast";
import { Fact, StateBadge } from "@/components/status-badge";
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
  const [eventsFor, setEventsFor] = useState<PortalFleetRow | null>(null);
  const [events, setEvents] = useState<PortalTLSEvent[] | null>(null);
  const [eventsErr, setEventsErr] = useState<unknown>(null);
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

  async function openEvents(r: PortalFleetRow) {
    setEventsFor(r);
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

  const revocable = (r: PortalFleetRow) => r.certificates.some((c) => (c.status === "ISSUED" || c.status === "SUPERSEDED") && !c.revoked);

  return (
    <PageShell width="wide">
      <PageHeader
        title="Portal certificates"
        description="Every appliance's portal hostname, its public DNS record and its HTTPS certificates. Appliances generate their own keys; Central only answers the CA's DNS challenge."
        help={
          <>
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
          <Card>
            <CardHeader>
              <div className="space-y-0.5">
                <CardTitle>ACME and DNS</CardTitle>
                <CardDescription>Which CA issues, who it writes to, and the zone Central publishes in.</CardDescription>
              </div>
            </CardHeader>
            <CardBody className="space-y-4">
              <ErrorBanner err={settingsErr} />
              <Field
                label="ACME directory"
                hint={
                  draft.acme_directory === "production"
                    ? "Production certificates are trusted by browsers and count against Let's Encrypt's real limits."
                    : "Staging certificates are not trusted by browsers. Prove one issuance here first."
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
              <div className="grid gap-4 sm:grid-cols-2">
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
              </div>
              <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <Fact label="Directory URL"><span className="break-all font-mono text-xs">{settings.directory_url}</span></Fact>
                <Fact label="ACME account">{settings.account_url ? <MonoId value={settings.account_url} head={28} title="Account" /> : "Not registered yet"}</Fact>
                <Fact label="Zone id">{settings.zone_id ? <MonoId value={settings.zone_id} head={12} title="Zone id" /> : "Not verified yet"}</Fact>
                <Fact label="CAA">{settings.caa_applied_at ? `Applied ${formatDateTime(settings.caa_applied_at)}` : "Not applied"}</Fact>
              </dl>
            </CardBody>
            {canManage && (
              <CardFooter>
                <Button disabled={!dirty || savingSettings} onClick={onSaveSettings}>
                  {savingSettings ? "Saving…" : "Save settings"}
                </Button>
              </CardFooter>
            )}
          </Card>

          <Card>
            <CardHeader>
              <div className="space-y-0.5">
                <CardTitle>DNS API token</CardTitle>
                <CardDescription>Kept on Central only, in a root-only file. It is never shown again.</CardDescription>
              </div>
              <Badge tone={settings.token_set ? "ok" : "warn"} dot>{settings.token_set ? "Set" : "Not set"}</Badge>
            </CardHeader>
            <CardBody className="space-y-4">
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
                      value={token}
                      onChange={(e) => setToken(e.target.value)}
                      placeholder="Zone-scoped token: Zone Read, DNS Edit"
                    />
                  </Field>
                  <Button type="submit" variant="secondary" disabled={!token.trim() || savingToken}>
                    {savingToken ? "Storing…" : "Store token"}
                  </Button>
                </form>
              )}
              <ErrorBanner err={actionErr} />
              {testResult && (
                <dl className="grid grid-cols-1 gap-3 rounded-md border border-border bg-surface/50 p-3 sm:grid-cols-2">
                  <Fact label="Zone">{testResult.zone_name}</Fact>
                  <Fact label="Nameservers">{testResult.nameservers.length ? testResult.nameservers.join(", ") : "—"}</Fact>
                  <Fact label="ACME account"><MonoId value={testResult.account_url} head={28} title="Account" /></Fact>
                  <Fact label="Directory"><span className="break-all font-mono text-xs">{testResult.directory}</span></Fact>
                </dl>
              )}
            </CardBody>
            {canManage && (
              <CardFooter>
                <Button variant="secondary" disabled={testing} onClick={testAccess}>
                  {testing ? "Testing…" : "Test DNS access"}
                </Button>
                <Button variant="ghost" disabled={applyingCAA || !settings.available} onClick={() => { setActionErr(null); setConfirmCAA(true); }}>
                  Apply CAA
                </Button>
              </CardFooter>
            )}
          </Card>
        </div>
      ) : null}

      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle>Appliances</CardTitle>
            <CardDescription>One row per appliance with a portal hostname.</CardDescription>
          </div>
          <SearchInput value={query} onChange={setQuery} placeholder="Search serial, hostname, domain" label="Search portal hostnames" />
        </CardHeader>
        {fleet === null ? (
          <SkeletonRows rows={4} cols={6} />
        ) : visible.length === 0 ? (
          <EmptyState
            icon={<GlobeLock />}
            title={query ? "No appliances match" : "No portal hostnames yet"}
            hint={query ? undefined : "An activated appliance receives its hostname the first time it syncs."}
          />
        ) : (
          <Table aria-label="Portal certificates">
            <THead>
              <TR>
                <TH>Appliance</TH>
                <TH>Hostname</TH>
                <TH className="hidden md:table-cell">DNS</TH>
                <TH className="hidden lg:table-cell">Hotel domain</TH>
                <TH>Certificate</TH>
                <TH className="hidden xl:table-cell">Last error</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <tbody>
              {visible.map((r) => {
                const c = mainCert(r);
                const left = daysUntil(c?.not_after);
                return (
                  <TR key={r.appliance_id}>
                    <TD>
                      <Link href={`/appliances/${r.appliance_id}`} className="font-mono text-xs underline-offset-2 hover:underline">{r.serial}</Link>
                      <div className="text-caption text-muted-foreground">
                        {[r.customer_name, r.site_name].filter(Boolean).join(" · ") || "—"}
                      </div>
                    </TD>
                    <TD>
                      <div className="break-all font-mono text-xs">{r.hostname}</div>
                      <div className="text-caption text-muted-foreground">{r.portal_ip ?? "no address yet"}</div>
                    </TD>
                    <TD className="hidden md:table-cell"><StateBadge info={portalDNSInfo(r.dns_state)} /></TD>
                    <TD className="hidden lg:table-cell">
                      {r.custom_domain ? (
                        <>
                          <div className="break-all font-mono text-xs">{r.custom_domain}</div>
                          <StateBadge info={portalDomainInfo(r.custom_domain_status)} className="mt-1" />
                        </>
                      ) : <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD>
                      {c ? (
                        <div className="space-y-1">
                          <div className="flex flex-wrap items-center gap-1.5">
                            <StateBadge info={portalCertInfo(c.status, c.revoked)} />
                            {dirBadge(c)}
                          </div>
                          {c.not_after && (
                            <div className="text-caption text-muted-foreground">
                              Expires {formatDay(c.not_after)}
                              {left !== null && c.status === "ISSUED" ? ` (${left < 0 ? "expired" : `${left} days`})` : ""}
                            </div>
                          )}
                          {c.renew_after && c.status === "ISSUED" && (
                            <div className="text-caption text-muted-foreground">Renews after {formatDay(c.renew_after)}</div>
                          )}
                          {r.certificates.length > 1 && (
                            <div className="text-caption text-muted-foreground">
                              + {r.certificates.length - 1} more ({r.certificates.filter((x) => x !== c).map((x) => portalCertInfo(x.status, x.revoked).label.toLowerCase()).join(", ")})
                            </div>
                          )}
                        </div>
                      ) : <span className="text-muted-foreground">None yet</span>}
                    </TD>
                    <TD className="hidden max-w-xs xl:table-cell">
                      {r.last_error ? <span className="line-clamp-3 text-xs text-destructive" title={r.last_error}>{r.last_error}</span> : <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD>
                      <div className="flex flex-wrap justify-end gap-1">
                        <Button size="sm" variant="ghost" onClick={() => openEvents(r)} aria-label={`Events for ${r.serial}`}>
                          <History /> Events
                        </Button>
                        {canManage && revocable(r) && (
                          <Button size="sm" variant="secondary" onClick={() => { setRevokeErr(null); setRevokeFor(r); }}>
                            Revoke
                          </Button>
                        )}
                      </div>
                    </TD>
                  </TR>
                );
              })}
            </tbody>
          </Table>
        )}
      </Card>

      <Sheet open={!!eventsFor} onOpenChange={(v) => { if (!v) setEventsFor(null); }}>
        <SheetContent width="lg">
          <SheetHeader
            icon={<History />}
            eyebrow="Portal certificate events"
            title={eventsFor?.serial ?? ""}
            description={eventsFor?.hostname}
          />
          <SheetBody>
            <ErrorBanner err={eventsErr} />
            {events === null ? (
              <SkeletonRows rows={5} cols={3} />
            ) : events.length === 0 ? (
              <EmptyState title="No events" className="py-8" />
            ) : (
              <ol className="divide-y divide-border">
                {events.map((e) => (
                  <li key={e.id} className="space-y-1 py-2.5">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="text-sm font-medium">{sentenceCase(e.kind.toLowerCase())}</span>
                      <time className="text-caption text-muted-foreground" dateTime={e.at} title={formatDateTime(e.at)}>{ago(e.at)}</time>
                    </div>
                    <div className="text-caption text-muted-foreground">
                      {[e.hostname, e.actor].filter(Boolean).join(" · ")}
                    </div>
                    {e.detail && Object.keys(e.detail).length > 0 && (
                      <code className="block break-all rounded bg-surface p-2 text-2xs text-muted-foreground">{JSON.stringify(e.detail)}</code>
                    )}
                  </li>
                ))}
              </ol>
            )}
          </SheetBody>
        </SheetContent>
      </Sheet>

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
