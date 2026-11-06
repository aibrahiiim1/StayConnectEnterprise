"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { cn, formatDate, formatRelative } from "@/lib/utils";
import {
  PortalHTTPSDoc, dnsWords, domainStatus, fallbackWords, looksLikePortalDomain, portalHeadline,
} from "@/lib/portal-https";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { Field, Input } from "@/components/ui/input";
import { DescriptionList } from "@/components/ui/data";
import { ChangeDiff, HistoryList } from "@/components/ui/history";
import { DetailsDrawer } from "@/components/ui/details-drawer";
import { Skeleton } from "@/components/ui/misc";
import { EmptyState } from "@/components/ui/empty-state";
import { CopyButton, ReadOnlyNotice } from "@/components/ui/patterns";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import { HelpList, HelpSection } from "@/components/help";
import { AlertTriangle, ShieldCheck, RefreshCw, RotateCw, Search, Globe } from "lucide-react";
import { useOperatorRoles } from "@/lib/whoami-context";

type Issue = { code: string; message: string };

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const LINK = "font-medium underline underline-offset-2";

const when = (s?: string | null, none = "Not set") => (s ? formatDate(s) : none);
const mono = (s?: string | null, none = "Not set") => (s ? <span className="break-all font-mono text-xs">{s}</span> : none);

const FIELD_LABEL: Record<string, string> = { portal_ip: "Portal address", custom_domain: "Portal domain" };
const fieldWords = (f: string) => FIELD_LABEL[f] ?? "A setting this console does not recognise";
const HISTORY_PREVIEW = 5;

function sentence(text: string): string {
  const t = text.trim();
  if (!t) return t;
  const s = t.charAt(0).toUpperCase() + t.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}

function portalError(e: unknown): string {
  const x = e instanceof ApiError ? e : null;
  switch (x?.code) {
    case "reauth_required":
      return "Your password was not accepted. Nothing was changed.";
    case "portal_address_required":
      return "Set the portal address first: the portal domain points at it. Nothing was changed.";
    case "scd_unreachable":
      return "The appliance's session service did not answer. Try again in a moment. Nothing was changed.";
    case "netd_unreachable":
      return "The appliance's network service could not check the address. Try again in a moment. Nothing was changed.";
    case "renew_refused":
      return `OneGate Central refused the renewal${x.message ? `: ${sentence(x.message)}` : "."} The current certificates keep serving.`;
    case "validation":
      return x.message ? `${sentence(x.message)} Nothing was changed.` : "That value cannot be used. Nothing was changed.";
    default:
      return "The appliance could not complete this. Check it is reachable, then try again. Nothing was changed.";
  }
}

function Reported({ text }: { text: string }) {
  return <span><span className="text-muted-foreground">OneGate Central reported: </span>{text}</span>;
}

export default function PortalHTTPSPage() {
  const toast = useToast();
  const [doc, setDoc] = useState<PortalHTTPSDoc | null>(null);
  const roles = useOperatorRoles();
  const [loadFailed, setLoadFailed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<null | "check" | "renew" | "address" | "domain" | "remove">(null);

  const [editingAddress, setEditingAddress] = useState(false);
  const [addressInput, setAddressInput] = useState("");
  const [addressErr, setAddressErr] = useState<string | null>(null);
  const [addressIssues, setAddressIssues] = useState<Issue[]>([]);
  const [savedNotLive, setSavedNotLive] = useState(false);

  const [domainInput, setDomainInput] = useState("");
  const [domainTouched, setDomainTouched] = useState(false);
  const [confirmDomain, setConfirmDomain] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [renewing, setRenewing] = useState(false);
  const [dialogErr, setDialogErr] = useState<string | null>(null);
  const [allHistory, setAllHistory] = useState(false);
  const [openChange, setOpenChange] = useState<string | null>(null);

  const writable = roles !== null && canWrite("portal-https", roles);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setDoc(await api.get<PortalHTTPSDoc>("/portal-https"));
      setLoadFailed(false);
    } catch {
      setLoadFailed(true);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function checkNow() {
    setBusy("check"); setErr(null);
    try {
      const r = await api.post<{ refreshed: boolean; portal: PortalHTTPSDoc }>("/portal-https/check", {});
      setDoc(r.portal);
      if (r.refreshed) toast.success("Checked with OneGate Central", "The state below is current.");
      else toast.success("Check requested", "OneGate Central has not answered yet; the page shows the last known state.");
    } catch (e) { setErr(portalError(e)); }
    finally { setBusy(null); }
  }

  async function saveAddress({ password }: { password: string }) {
    setBusy("address"); setAddressErr(null); setAddressIssues([]);
    try {
      const r = await api.put<{ changed: boolean; apply_required: boolean }>("/portal-https/address", {
        portal_ip: addressInput.trim(), password,
      });
      setEditingAddress(false);
      if (r.changed) setSavedNotLive(r.apply_required);
      if (r.changed) toast.success("Portal address saved", "Apply the network configuration on Client networks to make it live.");
      else toast.success("No change", "That is already the portal address.");
      await load();
    } catch (e) {
      const issues = e instanceof ApiError && Array.isArray(e.body?.issues) ? (e.body.issues as Issue[]) : [];
      setAddressIssues(issues);
      setAddressErr(issues.length > 0 ? null : portalError(e));
    } finally { setBusy(null); }
  }

  async function saveDomain({ password }: { password: string }) {
    setBusy("domain"); setDialogErr(null);
    try {
      const r = await api.put<{ changed: boolean; portal: PortalHTTPSDoc }>("/portal-https/custom-domain", {
        fqdn: domainInput.trim(), password,
      });
      setDoc(r.portal);
      setConfirmDomain(false);
      setDomainInput("");
      setDomainTouched(false);
      if (r.changed === false) toast.success("No change", "That is already the portal domain.");
      else toast.success("Portal domain saved", "Publish the DNS records below, then use Check now.");
    } catch (e) {
      setDialogErr(portalError(e));
    } finally { setBusy(null); }
  }

  async function removeDomain({ password }: { password: string }) {
    setBusy("remove"); setDialogErr(null);
    try {
      const r = await api.del<{ portal: PortalHTTPSDoc }>("/portal-https/custom-domain", { password });
      setDoc(r.portal);
      setRemoving(false);
      toast.success("Portal domain removed");
    } catch (e) {
      setDialogErr(portalError(e));
    } finally { setBusy(null); }
  }

  async function renew({ password }: { password: string }) {
    setBusy("renew"); setDialogErr(null);
    try {
      await api.post("/portal-https/renew", { password });
      setRenewing(false);
      toast.success("Renewal requested", "New certificates are being ordered. This usually takes a minute or two.");
      await load();
    } catch (e) {
      setDialogErr(portalError(e));
    } finally { setBusy(null); }
  }

  const st = doc?.state;
  const headline = doc ? portalHeadline(doc) : null;
  const domain = doc?.settings.custom_domain ?? null;
  const configured = doc?.settings.configured ?? false;
  const dStatus = domainStatus(st && st.custom_domain === domain ? st.custom_domain_status : "");
  const records = st && st.custom_domain === domain ? st.custom_domain_records ?? [] : [];
  const domainProblem = domainInput.trim() ? looksLikePortalDomain(domainInput) : null;
  const showDomainProblem = domainTouched && domainProblem !== null;
  const historyRows = doc ? (allHistory ? doc.history : doc.history.slice(0, HISTORY_PREVIEW)) : [];
  const change = doc?.history.find((h) => String(h.id) === openChange) ?? null;
  const applyNeeded = savedNotLive || (st?.fallback_reason === "PORTAL_ADDRESS_NOT_ACTIVE" && !st.https_active);
  const spin = cn(loading && "animate-spin motion-reduce:animate-none");

  return (
    <PageShell>
      <PageHeader
        title="Portal HTTPS"
        description="The Client Portal's own address, its host names and its certificates."
        help={
          <>
            <HelpSection title="What this is">
              <p>
                Clients reach the Client Portal over HTTPS at a host name with a publicly trusted certificate. The
                certificate&rsquo;s key is created on this appliance and never leaves it; OneGate Central obtains and renews
                the certificate.
              </p>
            </HelpSection>
            <HelpSection title="When HTTPS is not available">
              <HelpList
                items={[
                  "The portal keeps working over HTTP: clients can still sign in with a code.",
                  "Emails then carry the code only, without a link.",
                  "Social sign-in is not offered, because providers accept HTTPS callbacks only.",
                  "HTTPS comes back by itself once a valid certificate is installed again.",
                ]}
              />
            </HelpSection>
            <HelpSection title="Your domain">
              <p>
                Optional, and required for Google sign-in. Enter a subdomain you own (for example
                <code> wifi.example.com</code>), publish the three DNS records shown, then use <strong>Check now</strong>.
              </p>
            </HelpSection>
          </>
        }
        actions={
          <>
            {doc && (
              <Button variant="secondary" disabled={loading} onClick={() => void load()}>
                <RefreshCw className={spin} /> Refresh
              </Button>
            )}
            {writable && doc && (
              <>
                <Button variant="secondary" disabled={busy !== null} onClick={() => { setDialogErr(null); setRenewing(true); }}>
                  <RotateCw /> Renew now…
                </Button>
                <Button disabled={busy !== null} onClick={checkNow}>
                  <Search /> {busy === "check" ? "Checking…" : "Check now"}
                </Button>
              </>
            )}
          </>
        }
      />

      {roles !== null && !writable && (
        <ReadOnlyNotice>Your role can view the portal&rsquo;s HTTPS state but not change it.</ReadOnlyNotice>
      )}

      {err && (
        <div className={FADE}>
          <Callout tone="danger">{err}</Callout>
        </div>
      )}

      {loadFailed && doc && (
        <div className={FADE}>
          <Callout tone="warning">The page could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}

      {applyNeeded && (
        <div className={FADE}>
          <Callout tone="warning" title="Apply the network configuration">
            The portal address is saved but not live yet. Open <Link className={LINK} href="/network">Client
            networks</Link>, then Validate, Apply and keep the change.
          </Callout>
        </div>
      )}

      {!doc ? (
        loadFailed ? (
          <div role="alert" className={FADE}>
            <EmptyState
              icon={<AlertTriangle />}
              title="Portal HTTPS could not be loaded"
              hint="Check the appliance is reachable, then try again. Nothing has been changed."
              action={
                <Button variant="secondary" onClick={() => void load()} disabled={loading}>
                  <RefreshCw className={spin} /> Try again
                </Button>
              }
            />
          </div>
        ) : (
          <div className="space-y-3" aria-busy="true">
            <span className="sr-only">Loading</span>
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-40 w-full" />
          </div>
        )
      ) : (
        <>
          <div key={headline!.title} className={FADE}>
            <Callout tone={headline!.tone} title={headline!.title}>
              {!st!.https_active && (
                <>
                  {fallbackWords(st!.fallback_reason)}
                  {st!.fallback_since && <> Since {when(st!.fallback_since)}.</>}{" "}
                  Clients can still sign in with a code over HTTP; emails carry the code only and social sign-in is not offered.
                </>
              )}
              {st!.https_active && doc.health.level !== "ok" && doc.health.message}
            </Callout>
          </div>

          <div className="grid gap-5 lg:grid-cols-2">
            <SectionCard title="Host names" description="The canonical name is used in links and redirects.">
              <DescriptionList
                items={[
                  ...(st!.shared_hostname ? [{
                    label: "Client Portal address",
                    value: (
                      <span className="flex flex-wrap items-center gap-2">
                        {mono(st!.shared_hostname)}
                        {st!.https_active && st!.canonical_hostname === st!.shared_hostname && <Badge tone="accent">Canonical</Badge>}
                      </span>
                    ),
                    hint: "Shared by every OneGate appliance; guests always reach this appliance's own portal.",
                  }] : []),
                  {
                    label: "OneGate host name",
                    value: st!.onegate_hostname ? (
                      <span className="flex flex-wrap items-center gap-2">
                        {mono(st!.onegate_hostname)}
                        {st!.https_active && st!.canonical_hostname === st!.onegate_hostname && <Badge tone="accent">Canonical</Badge>}
                      </span>
                    ) : "Not assigned yet",
                    hint: st!.onegate_hostname ? `DNS: ${dnsWords(st!.dns_state)}` : undefined,
                  },
                  {
                    label: "Portal domain",
                    value: domain ? (
                      <span className="flex flex-wrap items-center gap-2">
                        {mono(domain)}
                        <Badge tone={dStatus.tone}>{dStatus.label}</Badge>
                        {st!.https_active && st!.canonical_hostname === domain && <Badge tone="accent">Canonical</Badge>}
                      </span>
                    ) : "None",
                  },
                  { label: "Last checked with Central", value: st!.last_sync_at ? formatRelative(st!.last_sync_at) : "Never" },
                  { label: "Last check error", value: st!.last_sync_error ? <Reported text={st!.last_sync_error} /> : "No error" },
                ]}
              />
            </SectionCard>

            <SectionCard
              title="Portal address"
              description="A private address inside the appliance, answered on client networks only."
              actions={writable && (
                <Button size="sm" variant="secondary" disabled={busy !== null}
                  onClick={() => { setAddressInput(doc.settings.portal_ip); setAddressErr(null); setAddressIssues([]); setEditingAddress(true); }}>
                  Change…
                </Button>
              )}
            >
              {configured ? (
                <DescriptionList
                  items={[
                    { label: "Address", value: mono(doc.settings.portal_ip) },
                    { label: "Last changed", value: `${when(doc.settings.updated_at, "Never")}${doc.settings.updated_by ? ` by ${doc.settings.updated_by}` : ""}` },
                  ]}
                />
              ) : (
                <Callout tone="warning" title="No portal address is set">
                  Every default address overlaps a client network on this appliance. Choose a private address that no
                  network here uses.
                </Callout>
              )}
            </SectionCard>
          </div>

          <SectionCard flush title="Certificates" description="Renewed automatically, each time with a new key.">
            {doc.certificates.length === 0 ? (
              <EmptyState size="sm" icon={<ShieldCheck />} title="No certificate yet" hint="One is requested as soon as this appliance has a host name." />
            ) : (
              <Table label="Portal certificates" aria-label="Portal certificates">
                <THead>
                  <TR><TH>Host name</TH><TH>Expires</TH><TH className="hidden sm:table-cell">Renews after</TH><TH className="hidden md:table-cell">Last error</TH></TR>
                </THead>
                <TBody>
                  {doc.certificates.map((c) => (
                    <TR key={c.hostname}>
                      <TD className="font-mono text-xs">
                        <span className="break-all">{c.hostname}</span>
                        {c.pending_since && <div className="font-sans text-xs text-muted-foreground">Order in progress since {when(c.pending_since)}</div>}
                      </TD>
                      <TD nowrap>{c.installed_at ? when(c.not_after, "Not known") : "Not installed yet"}</TD>
                      <TD nowrap muted className="hidden sm:table-cell">{when(c.renew_after, "Not scheduled")}</TD>
                      <TD truncate muted className="hidden md:table-cell" title={c.last_error ? `OneGate Central reported: ${c.last_error}` : undefined}>
                        {c.last_error ? <Reported text={c.last_error} /> : "No error"}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            )}
          </SectionCard>

          <SectionCard
            title="Portal domain"
            description="Optional. Needed for Google sign-in. A subdomain you own."
            actions={domain && writable && (
              <>
                <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => { setDialogErr(null); setRemoving(true); }}>
                  Remove…
                </Button>
                <Button size="sm" variant="secondary" disabled={busy !== null} onClick={checkNow}>
                  <Search /> {busy === "check" ? "Checking…" : "Check now"}
                </Button>
              </>
            )}
            bodyClassName="space-y-4"
          >
            {domain ? (
              <>
                <div className="flex flex-wrap items-center gap-2">
                  {mono(domain)} <StatusBadge tone={dStatus.tone}>{dStatus.label}</StatusBadge>
                </div>
                {st!.custom_domain_error && st!.custom_domain === domain && (
                  <Callout tone="danger" title="Verification problem"><Reported text={st!.custom_domain_error} /></Callout>
                )}
                {records.length > 0 ? (
                  <div className={cn("space-y-2", FADE)}>
                    <p className="text-sm">Publish these three records at your DNS provider:</p>
                    <div className="overflow-hidden rounded-md border border-border">
                      <Table label="DNS records to publish" aria-label="DNS records to publish" density="compact">
                        <THead><TR><TH>Type</TH><TH>Name</TH><TH>Value</TH></TR></THead>
                        <TBody>
                          {records.map((rec, i) => (
                            <TR key={i}>
                              <TD nowrap className="font-mono text-xs">{rec.type}</TD>
                              <TD>
                                <div className="flex items-center gap-1">
                                  <span className="break-all font-mono text-xs">{rec.name}</span>
                                  <CopyButton value={rec.name} variant="ghost" size="xs" label="Copy" />
                                </div>
                              </TD>
                              <TD>
                                <div className="flex items-center gap-1">
                                  <span className="break-all font-mono text-xs">{rec.value}</span>
                                  <CopyButton value={rec.value} variant="ghost" size="xs" label="Copy" />
                                </div>
                              </TD>
                            </TR>
                          ))}
                        </TBody>
                      </Table>
                    </div>
                  </div>
                ) : (
                  <p className="text-sm text-muted-foreground">
                    The DNS records to publish appear here once OneGate Central has registered the domain. Use Check now.
                  </p>
                )}
              </>
            ) : writable ? (
              <form
                className="flex flex-col gap-2 sm:flex-row sm:items-end"
                onSubmit={(e) => {
                  e.preventDefault();
                  setDomainTouched(true);
                  if (configured && !domainProblem && domainInput.trim()) { setDialogErr(null); setConfirmDomain(true); }
                }}
              >
                <Field
                  label="Domain"
                  hint={configured ? "For example wifi.example.com" : "Set the portal address first: the portal domain points at it."}
                  error={showDomainProblem ? domainProblem : undefined}
                  className="flex-1"
                >
                  <Input value={domainInput} onChange={(e) => setDomainInput(e.target.value)} onBlur={() => setDomainTouched(true)}
                    placeholder="wifi.example.com" autoComplete="off" spellCheck={false} disabled={!configured}
                    aria-invalid={showDomainProblem || undefined} />
                </Field>
                <Button type="submit" variant="secondary" disabled={busy !== null || !configured || !domainInput.trim() || showDomainProblem}>
                  <Globe /> Use this domain…
                </Button>
              </form>
            ) : (
              <p className="text-sm text-muted-foreground">No portal domain is set.</p>
            )}
          </SectionCard>

          <SectionCard flush title="Change history" description="Portal address and domain changes, newest first.">
            <HistoryList
              label="Portal HTTPS change history"
              emptyLabel="No changes yet."
              items={historyRows.map((h) => ({
                id: String(h.id),
                title: `${fieldWords(h.field)} changed`,
                actor: h.changed_by,
                at: when(h.changed_at, "Not recorded"),
                summary: `${h.old_value ?? "Not set"} → ${h.new_value ?? "Not set"}`,
              }))}
              selectedId={openChange}
              onSelect={(it) => setOpenChange(it.id)}
            />
            {doc.history.length > HISTORY_PREVIEW && (
              <div className="border-t border-border px-4 py-2.5">
                <Button size="sm" variant="ghost" onClick={() => setAllHistory((v) => !v)}>
                  {allHistory ? "Show fewer" : `View all ${doc.history.length.toLocaleString()}`}
                </Button>
              </div>
            )}
          </SectionCard>

          <DetailsDrawer
            open={change !== null}
            onOpenChange={(v) => !v && setOpenChange(null)}
            eyebrow="Portal HTTPS change"
            title={change ? `${fieldWords(change.field)} changed` : ""}
            description={change ? `${when(change.changed_at, "Not recorded")} · ${change.changed_by}` : undefined}
          >
            {change && (
              <ChangeDiff
                changes={[{
                  field: fieldWords(change.field),
                  before: change.old_value ? <span className="font-mono text-xs">{change.old_value}</span> : undefined,
                  after: change.new_value ? <span className="font-mono text-xs">{change.new_value}</span> : undefined,
                }]}
              />
            )}
          </DetailsDrawer>
        </>
      )}

      <ConfirmDialog
        open={editingAddress}
        onOpenChange={(v) => { if (!v) setEditingAddress(false); }}
        title="Change the portal address?"
        description="A private address on this appliance that clients' devices reach the portal at."
        consequences={[
          "Nothing changes for clients until you apply the network configuration on Client networks.",
          "OneGate Central updates the portal host name to point at the new address.",
        ]}
        confirmLabel="Save address"
        busy={busy === "address"}
        error={addressErr}
        requirePassword strictPassword
        onConfirm={saveAddress}
      >
        <Field label="Portal address" required hint="An address in 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16 that no network here uses.">
          <Input value={addressInput} onChange={(e) => setAddressInput(e.target.value)} placeholder="10.255.255.1"
            className="font-mono" autoComplete="off" spellCheck={false} />
        </Field>
        {addressIssues.length > 0 && (
          <ul role="alert" className="space-y-1 text-sm text-destructive" aria-label="Problems with the address">
            {addressIssues.map((i, k) => <li key={k}>{sentence(i.message)}</li>)}
          </ul>
        )}
      </ConfirmDialog>

      <ConfirmDialog
        open={confirmDomain}
        onOpenChange={(v) => { if (!v) setConfirmDomain(false); }}
        title={`Use ${domainInput.trim().toLowerCase()} for the portal?`}
        description="OneGate Central registers the domain and gives you three DNS records to publish."
        confirmLabel="Use this domain"
        busy={busy === "domain"}
        error={dialogErr}
        requirePassword strictPassword
        onConfirm={saveDomain}
      />

      <ConfirmDialog
        open={removing}
        onOpenChange={(v) => { if (!v) setRemoving(false); }}
        title="Remove the portal domain?"
        consequences={[
          "Its certificate is revoked and the portal goes back to the OneGate host name.",
          "Google sign-in stops being offered.",
        ]}
        confirmLabel="Remove domain"
        confirmVariant="danger"
        busy={busy === "remove"}
        error={dialogErr}
        requirePassword strictPassword
        onConfirm={removeDomain}
      />

      <ConfirmDialog
        open={renewing}
        onOpenChange={(v) => { if (!v) setRenewing(false); }}
        title="Renew the portal certificates now?"
        description="New certificates are ordered with new keys. This usually takes a minute or two. The current ones keep serving until the new ones are installed."
        confirmLabel="Renew now"
        busy={busy === "renew"}
        error={dialogErr}
        requirePassword strictPassword
        onConfirm={renew}
      />
    </PageShell>
  );
}
