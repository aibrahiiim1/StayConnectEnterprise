"use client";

// ONE PMS CONNECTION, IN FULL.
//
// Opened from its card, over the list, so an operator can move between connections without losing their place.
// Six tabs, each answering one question:
//
//   Overview        is it working, and if not, which of the checks is failing and what is waiting?
//   Configuration   what is live, what is drafted, and how the link recovers from a drop;
//   Credentials     (only for a system that signs in with a key) is one stored — never what it is;
//   Guest networks  which Wi-Fi networks resolve their guests against it;
//   History         every saved version, where it came from, and putting an older one back;
//   Actions         activate / pause / wind down, test the connection, reload the guest list.
//
// Everything that changes what happens to guests — publishing, activating, pausing, storing a credential,
// testing, reloading the guest list — asks for the operator's own password, because edged requires it.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, type PmsConnectionSettings, type PmsGuestNetworkRoute, type PmsInterfaceHealth, type PmsRevision } from "@/lib/api";
import { Sheet, SheetBody, SheetContent, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { KeyValueGrid, MetricStrip, Timeline } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { describePmsReadiness } from "@/lib/health-words";
import { formatDate } from "@/lib/utils";
import {
  type FormValues, type PmsConnection, type PmsProvider, type TestConnectionResult,
  CONTINUITY_WORDS, LIFECYCLE_REASONS, LIFECYCLE_WORDS, PUBLISH_REASONS, REASON_WORDS, ROOM_AUTH_WORDS,
  SECRET_REASONS, SYNC_WORDS, TRANSPORT_KIND_WORDS, TRANSPORT_WORDS,
  canFullResync, canTestConnection, credentialSecret, fieldsFor, isNeverConfigured, lastCommunication, needsCredential,
  pmsErrorText, providerRevisionBody, safeHost, validateAll, valuesFromRevision, verificationWords, wordsFor,
} from "@/lib/api/pms-connections";
import { ChecksRow, ProviderTile, When, lifecycleTone, providerLabel } from "./connection-card";
import { ConfigSummary, ProviderFieldsForm, configRows } from "./provider-fields";
import { SynchronizationCard } from "./synchronization-card";
import { Router } from "lucide-react";

export type SheetTab = "overview" | "configuration" | "credentials" | "networks" | "history" | "actions";

const UNKNOWN_PROVIDER = (kind: string): PmsProvider => ({
  kind, label: "Unrecognised system", credential: { mode: "NONE", fields: [] }, fields: [], capabilities: {},
});

export function ConnectionSheet({
  open, onOpenChange, iface, health, provider, writable, tab, onTab, onChanged,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  iface: PmsConnection | null;
  health?: PmsInterfaceHealth;
  provider: PmsProvider | null;
  writable: boolean;
  tab: SheetTab;
  onTab: (t: SheetTab) => void;
  onChanged: () => void | Promise<void>;
}) {
  const id = iface?.id ?? null;
  const [revisions, setRevisions] = useState<PmsRevision[] | null>(null);
  const [provenance, setProvenance] = useState("AVAILABLE");
  const [routes, setRoutes] = useState<PmsGuestNetworkRoute[] | null>(null);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    if (!id) return;
    try {
      const [r, d] = await Promise.all([
        api.get<{ revisions: PmsRevision[]; provenance_status?: string }>(`/pms-interfaces/${id}/revisions`),
        api.get<{ guest_networks?: PmsGuestNetworkRoute[] }>(`/pms-interfaces/${id}`),
      ]);
      setRevisions(r.revisions ?? []);
      setProvenance(r.provenance_status ?? "AVAILABLE");
      setRoutes(d.guest_networks ?? []);
      setErr(null);
    } catch (e) {
      setErr(e);
      setRevisions((x) => x ?? []);
      setRoutes((x) => x ?? []);
    }
  }, [id]);

  useEffect(() => {
    setRevisions(null); setRoutes(null); setErr(null);
    if (open && id) void load();
  }, [open, id, load]);

  const reloadAll = useCallback(async () => { await load(); await onChanged(); }, [load, onChanged]);

  if (!iface) return null;
  const p = provider ?? UNKNOWN_PROVIDER(iface.connector_kind);
  const retired = iface.lifecycle_state === "DECOMMISSIONED";
  const showCreds = needsCredential(p);
  const verification = verificationWords(iface.verification ?? p.verification);
  const activeTab = tab === "credentials" && !showCreds ? "overview" : tab;

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent width="xl">
        <SheetHeader
          icon={<ProviderTile label={providerLabel(iface, provider)} bare />}
          eyebrow={providerLabel(iface, provider)}
          title={iface.display_label || "Unnamed connection"}
          description={[p.vendor, p.integration, iface.transport || p.transport
            ? TRANSPORT_KIND_WORDS[(iface.transport || p.transport) as string] : null, safeHost(iface.endpoint)]
            .filter(Boolean).join(" · ") || undefined}
          badges={
            <>
              <Badge tone={lifecycleTone(iface.lifecycle_state)} dot>
                {wordsFor(LIFECYCLE_WORDS, iface.lifecycle_state, "State not recognised")}
              </Badge>
              <Badge tone={iface.published ? "neutral" : "default"}>
                {iface.published && iface.current_revision_no ? `Version ${iface.current_revision_no} live` : "Nothing published"}
              </Badge>
              <Badge tone={verification.tone} className="whitespace-normal">{verification.label}</Badge>
            </>
          }
        />
        <Tabs value={activeTab} onValueChange={(v) => onTab(v as SheetTab)} className="flex min-h-0 flex-1 flex-col">
          <div className="shrink-0 overflow-x-auto px-5">
            <TabsList className="min-w-max">
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="configuration">Configuration</TabsTrigger>
              {showCreds && <TabsTrigger value="credentials">Credentials</TabsTrigger>}
              <TabsTrigger value="networks">Guest networks</TabsTrigger>
              <TabsTrigger value="history">History</TabsTrigger>
              <TabsTrigger value="actions">Actions</TabsTrigger>
            </TabsList>
          </div>
          <SheetBody>
            <ErrorBanner err={pmsErrorText(err)} />
            <TabsContent value="overview" className="space-y-5">
              <OverviewTab iface={iface} health={health} provider={p} routes={routes} />
            </TabsContent>
            <TabsContent value="configuration" className="space-y-5">
              <ConfigurationTab iface={iface} provider={p} revisions={revisions} provenance={provenance}
                writable={writable && !retired} onChanged={reloadAll} />
            </TabsContent>
            {showCreds && (
              <TabsContent value="credentials" className="space-y-5">
                <CredentialsTab iface={iface} provider={p} writable={writable && !retired} onChanged={reloadAll} />
              </TabsContent>
            )}
            <TabsContent value="networks" className="space-y-5">
              <NetworksTab routes={routes} />
            </TabsContent>
            <TabsContent value="history" className="space-y-5">
              <HistoryTab iface={iface} provider={p} revisions={revisions} provenance={provenance}
                writable={writable && !retired} onChanged={reloadAll} />
            </TabsContent>
            <TabsContent value="actions" className="space-y-5">
              <ActionsTab iface={iface} provider={p} health={health} revisions={revisions}
                writable={writable && !retired} onChanged={reloadAll} />
            </TabsContent>
          </SheetBody>
        </Tabs>
      </SheetContent>
    </Sheet>
  );
}

// ---------------------------------------------------------------- Overview

function OverviewTab({
  iface, health, provider, routes,
}: {
  iface: PmsConnection;
  health?: PmsInterfaceHealth;
  provider: PmsProvider;
  routes: PmsGuestNetworkRoute[] | null;
}) {
  if (!health && isNeverConfigured(iface)) {
    return (
      <Callout tone="neutral" title="Never configured — not connected to any PMS">
        This connection was added but has no live configuration, so the appliance does not contact any PMS through
        it. Configure it on Configuration, put the configuration live, then activate it from Actions.
      </Callout>
    );
  }
  if (!health) {
    return (
      <Callout tone={iface.lifecycle_state === "DECOMMISSIONED" ? "neutral" : "warning"} title="Status not available">
        {iface.lifecycle_state === "DECOMMISSIONED"
          ? "This connection is retired, so its live status is no longer followed."
          : "The connection's status could not be read just now. It is checked again automatically."}
      </Callout>
    );
  }
  const readiness = describePmsReadiness({
    transport: health.transport_status, sync: health.sync_status,
    roomAuthReady: health.room_auth_ready, inHouse: health.in_house_stays,
  });
  const active = iface.lifecycle_state === "ACTIVE";
  const reason = !health.room_auth_ready && health.room_auth_reason
    ? ROOM_AUTH_WORDS[health.room_auth_reason] ?? "Room sign-in is not available through this connection." : null;

  return (
    <>
      <Callout
        tone={!active ? "neutral" : readiness.tone === "ok" ? "success" : readiness.tone === "err" ? "danger" : "warning"}
        title={active ? readiness.headline : wordsFor(LIFECYCLE_WORDS, iface.lifecycle_state, "Not in use")}
      >
        {active
          ? <>{readiness.summary}{reason ? <> {reason}</> : null}</>
          : iface.lifecycle_state === "DRAINING"
            ? "No new room sign-ins are accepted through this connection while it winds down."
            : !iface.published
              ? "Never configured — not connected to any PMS. Publish a configuration and activate it to start."
              : "This connection is switched off, so nobody signs in with a room number through it. Activate it from Actions."}
      </Callout>

      <SheetSection title="The checks room sign-in depends on">
        <ChecksRow health={health} />
        <KeyValueGrid
          columns={2}
          items={[
            {
              label: "Connection",
              value: wordsFor(TRANSPORT_WORDS, health.transport_status),
              hint: health.transport_status === "CONNECTED"
                ? health.last_connected_at ? <>Connected <When at={health.last_connected_at} /></> : undefined
                : health.disconnected_since ? <>Down since <When at={health.disconnected_since} /></> : undefined,
            },
            {
              label: "Live updates",
              value: wordsFor(CONTINUITY_WORDS, health.continuity_status),
              hint: health.last_valid_event_at ? <>Last update <When at={health.last_valid_event_at} /></> : undefined,
            },
            {
              label: "Guest list",
              value: wordsFor(SYNC_WORDS, health.sync_status),
              hint: <>Last full refresh <When at={health.last_complete_sync_at} /></>,
            },
            {
              label: "Last heard from the PMS",
              value: <When at={lastCommunication(health)} />,
              hint: lastCommunication(health) ? formatDate(lastCommunication(health)!) : undefined,
            },
          ]}
        />
      </SheetSection>

      <SheetSection title="Guest list and backlog">
        <MetricStrip
          items={[
            { label: "Guests in house", value: (health.in_house_stays ?? 0).toLocaleString() },
            { label: "Last stay update", value: <span className="text-sm"><When at={health.last_stay_event_at} /></span> },
            {
              label: "Waiting to be applied",
              value: (health.pending_events ?? 0).toLocaleString(),
              tone: (health.pending_events ?? 0) > 0 ? "warn" : undefined,
            },
            {
              label: "Need a decision",
              value: (health.review_events ?? 0).toLocaleString(),
              tone: (health.review_events ?? 0) > 0 ? "warn" : undefined,
            },
          ]}
        />
        {health.oldest_pending_at && (health.pending_events ?? 0) > 0 && (
          <p className="text-xs text-muted-foreground">
            Oldest waiting message arrived <When at={health.oldest_pending_at} />. A large backlog is a busy
            moment; an old one means processing is stuck.
          </p>
        )}
        {(health.review_events ?? 0) > 0 ? (
          <Callout tone="warning" title="PMS messages need a decision">
            {health.review_events.toLocaleString()} recorded PMS message{health.review_events === 1 ? "" : "s"} could
            not be matched to a stay. Sign-in continues from the guest list already in use; what these messages
            would have changed has not been applied.{" "}
            <Link href="/stay-events" className="font-medium underline underline-offset-2">Review stay events</Link>
            {" · "}
            <Link href="/pms-reconciliation" className="font-medium underline underline-offset-2">Unresolved departures</Link>
          </Callout>
        ) : (
          <p className="text-xs text-muted-foreground">
            Every PMS message has been matched or answered. Reconciliation runs automatically after each complete
            guest list.
          </p>
        )}
      </SheetSection>

      <SheetSection title="Investigate further" description="For troubleshooting. Nothing here needs checking day to day.">
        <ul className="grid gap-2 text-sm sm:grid-cols-2">
          <li><Link href="/stay-events" className="font-medium underline underline-offset-2">Stay events</Link>
            <p className="text-xs text-muted-foreground">Every check-in, move and check-out received.</p></li>
          <li><Link href="/roster-reconciliation" className="font-medium underline underline-offset-2">Roster reconciliation</Link>
            <p className="text-xs text-muted-foreground">How the guest list is kept identical to the hotel&rsquo;s.</p></li>
          <li><Link href="/pms-reconciliation" className="font-medium underline underline-offset-2">Unresolved departures</Link>
            <p className="text-xs text-muted-foreground">Messages that could not be matched to a stay.</p></li>
          <li><Link href="/pms-source-conflicts" className="font-medium underline underline-offset-2">Source conflicts</Link>
            <p className="text-xs text-muted-foreground">Two connections that may be the same PMS.</p></li>
        </ul>
      </SheetSection>

      <SheetSection title="System">
        <KeyValueGrid
          columns={2}
          items={[
            { label: "Property management system", value: providerLabel(iface, provider) },
            { label: "Connects to", value: safeHost(iface.endpoint) ?? "—" },
            { label: "Link", value: (iface.transport || provider.transport) ? TRANSPORT_KIND_WORDS[(iface.transport || provider.transport) as string] ?? "—" : "—" },
            { label: "Guest networks", value: routes === null ? "—" : routes.length === 0 ? "None" : routes.map((r) => r.guest_network_name || "Unnamed network").join(", ") },
            {
              label: "Verification",
              value: verificationWords(iface.verification ?? provider.verification).label,
              hint: provider.verification_note, wide: true,
            },
          ]}
        />
      </SheetSection>
    </>
  );
}

// ---------------------------------------------------------------- Configuration

function PublishDialog({
  iface, target, onClose, onDone,
}: {
  iface: PmsConnection;
  target: PmsRevision | null;
  onClose: () => void;
  onDone: () => void | Promise<void>;
}) {
  const toast = useToast();
  const [reason, setReason] = useState(PUBLISH_REASONS[0].value);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const older = !!target && !!iface.current_revision_no && target.revision_no < iface.current_revision_no;

  useEffect(() => {
    if (target) {
      setErr(null);
      setReason(older ? "CONFIG_ROLLBACK" : iface.published ? "CONFIG_CORRECTION" : "INITIAL_COMMISSIONING");
    }
  }, [target, older, iface.published]);

  return (
    <ConfirmDialog
      open={target !== null}
      onOpenChange={(v) => !v && onClose()}
      title={target ? `Put version ${target.revision_no} live?` : ""}
      description={
        iface.lifecycle_state === "ACTIVE"
          ? "From this moment the connection uses these settings and reconnects with them. Guests keep being verified against the guest list already loaded while it does."
          : "This becomes the configuration the connection uses. It does not activate the connection — that is a separate step."
      }
      confirmLabel="Put live"
      busy={busy}
      error={pmsErrorText(err)}
      requirePassword
      onConfirm={async ({ password }) => {
        if (!target) return;
        setBusy(true); setErr(null);
        try {
          await api.post(`/pms-interfaces/${iface.id}/publish`, {
            revision_id: target.id,
            // The version this operator believed was live. If it changed while the dialog was open, edged refuses
            // rather than silently reverting whoever published in between.
            expected_revision_id: iface.current_revision_id ?? "",
            reason_code: reason,
            password,
          });
          toast.success(`Version ${target.revision_no} is live`,
            iface.lifecycle_state === "ACTIVE" ? "The connection reconnects with it." : "Activate the connection when you are ready.");
          onClose();
          await onDone();
        } catch (e) {
          setErr(e);
        } finally {
          setBusy(false);
        }
      }}
    >
      <Field label="Reason" hint="Recorded with the change.">
        <Select value={reason} onChange={(e) => setReason(e.target.value)}>
          {PUBLISH_REASONS.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
        </Select>
      </Field>
    </ConfirmDialog>
  );
}

function orderRevisions(revisions: PmsRevision[] | null) {
  const ordered = (revisions ?? []).slice().sort((a, b) => b.revision_no - a.revision_no);
  const current = ordered.find((r) => r.published) ?? null;
  const drafts = ordered.filter((r) => !r.published && (!current || r.revision_no > current.revision_no));
  return { ordered, current, drafts };
}

function ConfigurationTab({
  iface, provider, revisions, provenance, writable, onChanged,
}: {
  iface: PmsConnection;
  provider: PmsProvider;
  revisions: PmsRevision[] | null;
  provenance: string;
  writable: boolean;
  onChanged: () => void | Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [publishing, setPublishing] = useState<PmsRevision | null>(null);
  const { ordered, current, drafts } = orderRevisions(revisions);

  if (revisions === null) return <Skeleton className="h-40 w-full" />;

  return (
    <>
      <SheetSection
        title="Live configuration"
        actions={writable && (
          <Button size="sm" variant="secondary" onClick={() => setEditing(true)}>
            {current ? "Edit configuration" : ordered.length ? "Edit the draft" : "Configure"}
          </Button>
        )}
      >
        {!current ? (
          <Callout tone="neutral" title="Nothing is live yet">
            {drafts.length
              ? "A draft is saved below. Put it live to make it the configuration this connection uses."
              : "No configuration has been saved. Configure the connection to create a draft."}
          </Callout>
        ) : (
          <div className="space-y-3 rounded-lg border border-border p-4">
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone="ok">Version {current.revision_no}</Badge>
              <Badge tone="neutral">In use</Badge>
            </div>
            <VersionProvenance rev={current} unavailable={provenance === "UNAVAILABLE"} />
            <ConfigSummary provider={provider} rev={current} />
          </div>
        )}
      </SheetSection>

      {drafts.length > 0 && (
        <SheetSection title="Drafts" description="Saved but not live. Nothing changes for guests until one is put live.">
          <div className="space-y-3">
            {drafts.map((d) => (
              <div key={d.id} className="space-y-3 rounded-lg border border-dashed border-border p-4">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <Badge tone="info">Version {d.revision_no}</Badge>
                    <span className="text-xs text-muted-foreground">Draft</span>
                  </div>
                  {writable && (
                    <div className="flex flex-wrap gap-2">
                      {canTestConnection(provider) && <TestConnectionButton iface={iface} provider={provider} revision={d} />}
                      <Button size="sm" onClick={() => setPublishing(d)}>Put version {d.revision_no} live</Button>
                    </div>
                  )}
                </div>
                <ChangeSummary rev={d} prev={current ?? undefined} provider={provider} />
                <details>
                  <summary className="cursor-pointer text-xs text-muted-foreground">All settings in this draft</summary>
                  <div className="mt-2"><ConfigSummary provider={provider} rev={d} /></div>
                </details>
              </div>
            ))}
          </div>
        </SheetSection>
      )}

      <ConnectionRecovery id={iface.id} writable={writable} />

      <RevisionDialog
        open={editing}
        onOpenChange={setEditing}
        iface={iface}
        provider={provider}
        from={current ?? ordered[0] ?? null}
        onDone={onChanged}
      />
      <PublishDialog iface={iface} target={publishing} onClose={() => setPublishing(null)} onDone={onChanged} />
    </>
  );
}

function RevisionDialog({
  open, onOpenChange, iface, provider, from, onDone,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  iface: PmsConnection;
  provider: PmsProvider;
  /** The configuration to start from — the live one — so Edit means "change what is there". */
  from: PmsRevision | null;
  onDone: () => void | Promise<void>;
}) {
  const toast = useToast();
  const fields = useMemo(() => fieldsFor(provider), [provider]);
  const [values, setValues] = useState<FormValues>({});
  const [timezone, setTimezone] = useState("Africa/Cairo");
  const [showErrors, setShowErrors] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  useEffect(() => {
    if (!open) return;
    const v = valuesFromRevision(provider, from);
    setValues(v.values); setTimezone(v.timezone); setShowErrors(false); setErr(null);
  }, [open, provider, from]);

  const errors = validateAll(fields, values);

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      title={from ? "Edit configuration" : "Configure this connection"}
      description="Saved as a new draft version. Nothing changes for guests until you put it live, which is a separate confirmed step."
      submitLabel="Save as draft"
      busy={busy}
      error={pmsErrorText(err)}
      onSubmit={async () => {
        if (Object.keys(errors).length > 0 || !timezone.trim()) { setShowErrors(true); return; }
        setBusy(true); setErr(null);
        try {
          const r = await api.post<{ revision_no?: number }>(
            `/pms-interfaces/${iface.id}/revisions`, providerRevisionBody(provider, values, timezone));
          toast.success(`Saved as draft version ${r?.revision_no ?? ""}`.trim(), "Put it live from Configuration when you are ready.");
          onOpenChange(false);
          await onDone();
        } catch (e) { setErr(e); } finally { setBusy(false); }
      }}
    >
      <Field label="PMS time zone" required hint="Time zone name, for example Europe/Berlin. Arrivals and departures are read in it."
        error={showErrors && !timezone.trim() ? "Required." : undefined}>
        <Input value={timezone} spellCheck={false} onChange={(e) => setTimezone(e.target.value)} />
      </Field>
      <ProviderFieldsForm fields={fields} values={values} errors={showErrors ? errors : {}}
        onChange={(k, v) => setValues((s) => ({ ...s, [k]: v }))} disabled={busy} />
    </DialogForm>
  );
}

// WHAT ACTUALLY CHANGED between a version and the one before it — derived only from stored values. Where two
// versions carry identical settings this says so, and names any derived state that moved instead.
function ChangeSummary({ rev, prev, provider }: { rev: PmsRevision; prev?: PmsRevision; provider: PmsProvider }) {
  if (!prev) return <p className="text-sm text-muted-foreground">The first saved configuration.</p>;
  const a = configRows(provider, prev);
  const b = configRows(provider, rev);
  const changes = b
    .map((row, i) => ({ field: row.label, from: a[i]?.value ?? "—", to: row.value }))
    .filter((c) => c.from !== c.to);
  if (changes.length > 0) {
    return (
      <div className="text-sm">
        <p className="font-medium">Changed from version {prev.revision_no}:</p>
        <ul className="mt-1 space-y-0.5">
          {changes.map((c, i) => (
            <li key={i} className="text-muted-foreground">
              {c.field}: <span className="line-through">{c.from}</span> → {c.to}
            </li>
          ))}
        </ul>
      </div>
    );
  }
  const internal: string[] = [];
  if ((prev.source_fingerprint ?? "") !== (rev.source_fingerprint ?? "")) {
    internal.push((prev.source_fingerprint ?? "") === ""
      ? "The PMS message format was observed and recorded for the first time"
      : "The recorded PMS message format changed");
  }
  if (prev.normalization_version !== rev.normalization_version) internal.push("The message-handling version changed");
  if (prev.folio_identity_strategy !== rev.folio_identity_strategy) internal.push("The folio matching strategy changed");
  return (
    <div className="text-sm">
      <p className="font-medium">The settings are identical to version {prev.revision_no}.</p>
      {internal.length > 0 ? (
        <ul className="mt-1 space-y-0.5">{internal.map((t, i) => <li key={i} className="text-muted-foreground">{t}</li>)}</ul>
      ) : (
        <p className="text-muted-foreground">Nothing else recorded explains why it was saved.</p>
      )}
    </div>
  );
}

function VersionProvenance({ rev, unavailable }: { rev: PmsRevision; unavailable?: boolean }) {
  const when = rev.published_at ?? rev.authored_at;
  // NEVER INVENTED, and never conflated: "nothing was recorded" and "the record could not be read" look the same
  // in the data and mean opposite things.
  if (unavailable) {
    return (
      <p className="text-xs text-warning-subtle-foreground">
        The record of how this version was saved could not be read just now, so it is not shown. This is a fault
        to report, not a sign that nothing was recorded — the configuration itself is unaffected.
      </p>
    );
  }
  if (!when && !rev.actor_id && !rev.reason_code) {
    return <p className="text-xs text-muted-foreground">How this version came to be saved was not recorded.</p>;
  }
  return (
    <ul className="space-y-0.5 text-xs text-muted-foreground">
      {when && <li>Saved {formatDate(when)}</li>}
      {(rev.actor_label || rev.actor_id) && (
        <li>By {rev.actor_label || "an operator who is no longer on this appliance"}</li>
      )}
      {rev.reason_code && <li>Reason: {REASON_WORDS[rev.reason_code] ?? "Not described"}</li>}
    </ul>
  );
}

// CONNECTION RECOVERY — how the link retries after a drop, for THIS connection only.
const CONN_FIELDS: {
  key: Extract<keyof PmsConnectionSettings, string>;
  label: string;
  unit: string;
  what: string;
  when: string;
}[] = [
  {
    key: "backoff_min_ms", label: "Shortest wait before retrying", unit: "milliseconds",
    what: "After the PMS link drops, how soon the first reconnection attempt happens. Each further attempt waits a little longer, up to the maximum below.",
    when: "Raise it if the hotel's PMS complains about repeated connections during its nightly restart.",
  },
  {
    key: "backoff_max_ms", label: "Longest wait before retrying", unit: "milliseconds",
    what: "Attempts never get further apart than this. The appliance retries indefinitely — this caps the gap, never the number of tries.",
    when: "Lower it if the PMS restarts often and you want the guest list current again sooner.",
  },
  {
    key: "stable_reset_seconds", label: "Connection must hold this long to count as recovered", unit: "seconds",
    what: "Once a reconnection survives this long, the waiting resets to the shortest value.",
    when: "Raise it if the link reconnects and drops again within a minute or two.",
  },
  {
    key: "link_down_alert_seconds", label: "Report the link as down after", unit: "seconds",
    what: "How long the link may stay down before it is reported. Nothing stops when the link drops — guests keep signing in from the last good guest list.",
    when: "Lower it to hear about an outage sooner; raise it if nightly maintenance produces an alert nobody acts on.",
  },
  {
    key: "blocked_after_refusals", label: "Report reconciliation as blocked after", unit: "consecutive runs",
    what: "Reconciliation declines to act on an incomplete or contradictory guest list, which protects guests. After this many refusals in a row it is reported.",
    when: "Lower it to hear about a degrading feed sooner.",
  },
];

function ConnectionRecovery({ id, writable }: { id: string; writable: boolean }) {
  const toast = useToast();
  const [settings, setSettings] = useState<PmsConnectionSettings | null>(null);
  const [form, setForm] = useState<Record<string, number>>({});
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      setSettings(await api.get<PmsConnectionSettings>(`/pms-interfaces/${id}/connection-settings`));
    } catch {
      /* not every connection exposes recovery settings; the section is simply not shown */
    }
  }, [id]);
  useEffect(() => { void load(); }, [load]);

  if (!settings) return null;

  async function save() {
    setBusy(true); setErr(null);
    try {
      const out = await api.put<{ config_version: number }>(`/pms-interfaces/${id}/connection-settings`, { ...form, reason });
      toast.success("Recovery settings saved", `Version ${out.config_version}. They apply on the next reconnect.`);
      setForm({}); setReason("");
      await load();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <SheetSection
      title="Connection recovery"
      description="The link reconnects by itself and retries for as long as it takes. These bound how fast it retries and how long a problem may last before it is reported. Most properties never change them."
    >
      <ErrorBanner err={pmsErrorText(err)} />
      <p className="rounded-md border border-dashed border-border p-3 text-sm text-muted-foreground">
        <strong className="text-foreground">These apply to this connection only.</strong> Another PMS connection at
        this property keeps its own values and is unaffected by anything changed here.
      </p>
      <div className="grid gap-3 lg:grid-cols-2">
        {CONN_FIELDS.map((f) => (
          <div key={f.key} className="space-y-2 rounded-md border border-border p-3">
            <Field label={`${f.label} (${f.unit})`} hint={f.what}>
              <Input
                type="number"
                className="w-40"
                value={form[f.key] ?? settings[f.key] ?? ""}
                onChange={(e) => setForm({ ...form, [f.key]: Number(e.target.value) })}
                disabled={busy || !writable}
              />
            </Field>
            <p className="text-xs text-muted-foreground">
              <strong>Currently:</strong> {settings[f.key]} {f.unit}
              {settings.is_default ? " (nobody has changed this one)" : ""}
            </p>
            <p className="text-xs text-muted-foreground"><strong>Change it when:</strong> {f.when}</p>
          </div>
        ))}
      </div>
      {writable && (
        <div className="flex flex-wrap items-end gap-2">
          <Field label="Reason" className="min-w-[14rem] flex-1">
            <Input placeholder="Reason - recorded against this change" value={reason}
              onChange={(e) => setReason(e.target.value)} disabled={busy} />
          </Field>
          <Button onClick={() => void save()} disabled={busy || Object.keys(form).length === 0}>
            Save recovery settings
          </Button>
        </div>
      )}
    </SheetSection>
  );
}

// ---------------------------------------------------------------- Credentials

function CredentialsTab({
  iface, provider, writable, onChanged,
}: {
  iface: PmsConnection;
  provider: PmsProvider;
  writable: boolean;
  onChanged: () => void | Promise<void>;
}) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const [reason, setReason] = useState(SECRET_REASONS[1].value);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const stored = typeof iface.secret_generation === "number";
  const fields = provider.credential?.fields ?? [];
  const missing = fields.some((f) => f.required && !(values[f.key] ?? "").trim());

  useEffect(() => {
    if (open) { setValues({}); setErr(null); setReason(stored ? SECRET_REASONS[1].value : SECRET_REASONS[0].value); }
  }, [open, stored]);

  return (
    <>
      <SheetSection
        title="Credential"
        description="What the appliance presents to the PMS provider. It is stored encrypted and is never shown — not here and not to any other operator."
        actions={writable && (
          <Button size="sm" variant="secondary" onClick={() => setOpen(true)}>
            {stored ? "Replace credential" : "Store credential"}
          </Button>
        )}
      >
        {stored ? (
          <KeyValueGrid items={[
            { label: "Stored", value: `Yes — version ${iface.secret_generation}` },
            { label: "Last replaced", value: iface.secret_rotated_at ? <When at={iface.secret_rotated_at} /> : "Not replaced since it was first stored" },
          ]} />
        ) : (
          <Callout tone="warning" title="No credential stored">
            This connection cannot sign in to {provider.label} until one is stored.
          </Callout>
        )}
      </SheetSection>

      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={stored ? "Replace the credential?" : "Store the credential?"}
        description="The new credential is used from the next connection attempt. The previous one stops being used. Nothing typed here is ever shown again."
        confirmLabel={stored ? "Replace credential" : "Store credential"}
        busy={busy}
        error={pmsErrorText(err)}
        requirePassword
        onConfirm={async ({ password }) => {
          if (missing) { setErr("Fill in every required field."); return; }
          setBusy(true); setErr(null);
          try {
            await api.post(`/pms-interfaces/${iface.id}/secret`, {
              secret: credentialSecret(provider, values), reason_code: reason, password,
            });
            toast.success(stored ? "Credential replaced" : "Credential stored");
            setOpen(false);
            await onChanged();
          } catch (e) { setErr(e); } finally { setBusy(false); }
        }}
      >
        {fields.map((f) => (
          <Field key={f.key} label={f.label} required={f.required} hint={f.help}>
            <Input
              type={f.secret === false ? "text" : "password"}
              autoComplete={f.secret === false ? "off" : "new-password"}
              value={values[f.key] ?? ""}
              onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
            />
          </Field>
        ))}
        <Field label="Why is it changing?">
          <Select value={reason} onChange={(e) => setReason(e.target.value)}>
            {SECRET_REASONS.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
          </Select>
        </Field>
      </ConfirmDialog>
    </>
  );
}

// ---------------------------------------------------------------- Guest networks

function NetworksTab({ routes }: { routes: PmsGuestNetworkRoute[] | null }) {
  return (
    <SheetSection
      title="Guest networks using this connection"
      description="A guest is only checked against this PMS if they are on one of these networks."
      actions={<Link href="/pms-routing" className="text-sm font-medium underline underline-offset-2">Change routing</Link>}
    >
      {routes === null ? (
        <Skeleton className="h-16 w-full" />
      ) : routes.length === 0 ? (
        <Callout tone="warning" title="No Wi-Fi network points at this connection">
          It may be configured and connected, but no guest is ever checked against it. Point at least one guest
          network at it on{" "}
          <Link href="/pms-routing" className="underline underline-offset-2">Network routing</Link>.
        </Callout>
      ) : (
        <ul className="divide-y divide-border rounded-lg border border-border">
          {routes.map((r) => (
            <li key={r.guest_network_id} className="flex flex-wrap items-center gap-2 px-3 py-2.5 text-sm">
              <Router className="size-4 shrink-0 text-muted-foreground" aria-hidden />
              <span className="font-medium">{r.guest_network_name || "Unnamed network"}</span>
              {r.is_default && <Badge tone="info">Site default</Badge>}
              {r.routing_mode === "ALL_ACTIVE_INTERFACES" && <Badge tone="neutral">Checks every active connection</Badge>}
            </li>
          ))}
        </ul>
      )}
    </SheetSection>
  );
}

// ---------------------------------------------------------------- History

function HistoryTab({
  iface, provider, revisions, provenance, writable, onChanged,
}: {
  iface: PmsConnection;
  provider: PmsProvider;
  revisions: PmsRevision[] | null;
  provenance: string;
  writable: boolean;
  onChanged: () => void | Promise<void>;
}) {
  const [publishing, setPublishing] = useState<PmsRevision | null>(null);
  if (revisions === null) return <Skeleton className="h-40 w-full" />;
  const { ordered, current } = orderRevisions(revisions);

  return (
    <SheetSection
      title="Configuration history"
      description="Every saved version of this connection, newest first. They are kept permanently and cannot be edited or removed."
    >
      <Timeline
        emptyLabel="No configuration has been saved yet."
        items={ordered.map((rev) => {
          const older = ordered.find((o) => o.revision_no === rev.revision_no - 1);
          const isDraft = !rev.published && (!current || rev.revision_no > current.revision_no);
          return {
            key: rev.id,
            tone: rev.published ? "ok" : isDraft ? "info" : "neutral",
            title: (
              <span className="inline-flex items-center gap-2">
                Version {rev.revision_no}
                <Badge tone={rev.published ? "ok" : isDraft ? "info" : "default"}>
                  {rev.published ? "In use" : isDraft ? "Draft" : "Previous"}
                </Badge>
              </span>
            ),
            when: (rev.published_at ?? rev.authored_at) ? <When at={rev.published_at ?? rev.authored_at} /> : undefined,
            body: (
              <div className="space-y-2">
                <VersionProvenance rev={rev} unavailable={provenance === "UNAVAILABLE"} />
                <ChangeSummary rev={rev} prev={older} provider={provider} />
                <details>
                  <summary className="cursor-pointer text-xs">All settings in this version</summary>
                  <div className="mt-2"><ConfigSummary provider={provider} rev={rev} /></div>
                </details>
                {writable && !rev.published && (
                  <Button size="sm" variant="secondary" onClick={() => setPublishing(rev)}>
                    {isDraft ? `Put version ${rev.revision_no} live` : "Put this version back in use"}
                  </Button>
                )}
              </div>
            ),
          };
        })}
      />
      <PublishDialog iface={iface} target={publishing} onClose={() => setPublishing(null)} onDone={onChanged} />
    </SheetSection>
  );
}

// ---------------------------------------------------------------- Actions

type LifecycleMove = {
  to: "ACTIVE" | "AUTH_DISABLED" | "DRAINING";
  label: string;
  variant: "primary" | "secondary" | "danger";
  title: string;
  description: string;
};

function lifecycleMoves(iface: PmsConnection, providerName: string): LifecycleMove[] {
  const activate: LifecycleMove = {
    to: "ACTIVE", label: iface.lifecycle_state === "DRAINING" ? "Activate again" : "Activate", variant: "primary",
    title: "Activate this connection?",
    description: `The appliance connects to ${providerName} with the live configuration${iface.current_revision_no ? ` (version ${iface.current_revision_no})` : ""} and loads the guest list. Guests on the networks that use this connection can then sign in with their room number. Nothing is written to the PMS.`,
  };
  const pause: LifecycleMove = {
    to: "AUTH_DISABLED", label: "Pause room sign-in", variant: "danger",
    title: "Pause room sign-in through this connection?",
    description: "The appliance stops using this connection and stops connecting to the PMS. Guests on the networks that use it can no longer sign in with their room number. Guests already online stay online, recorded stays are kept, and vouchers and guest accounts keep working. You can activate it again at any time.",
  };
  const drain: LifecycleMove = {
    to: "DRAINING", label: "Wind down", variant: "secondary",
    title: "Wind this connection down?",
    description: "No new room sign-ins or room charges are accepted through this connection. Work already queued — such as charges waiting to be sent — is allowed to finish. Use this before replacing the connection. You can activate it again or pause it afterwards.",
  };
  switch (iface.lifecycle_state) {
    case "AUTH_DISABLED": return [activate];
    case "ACTIVE": return [drain, pause];
    case "DRAINING": return [activate, pause];
    default: return [];
  }
}

function ActionsTab({
  iface, provider, health, revisions, writable, onChanged,
}: {
  iface: PmsConnection;
  provider: PmsProvider;
  health?: PmsInterfaceHealth;
  revisions: PmsRevision[] | null;
  writable: boolean;
  onChanged: () => void | Promise<void>;
}) {
  const toast = useToast();
  const [move, setMove] = useState<LifecycleMove | null>(null);
  const [reason, setReason] = useState(LIFECYCLE_REASONS[0].value);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const moves = lifecycleMoves(iface, provider.label);
  const { drafts } = orderRevisions(revisions);
  const retired = iface.lifecycle_state === "DECOMMISSIONED";

  return (
    <>
      <SheetSection
        title="Room sign-in through this connection"
        description={
          retired ? "This connection is retired. That is permanent, so there is nothing to change here."
            : iface.lifecycle_state === "ACTIVE" ? "Active: the appliance is connected to the PMS and serves room sign-in through it."
              : iface.lifecycle_state === "DRAINING" ? "Winding down: no new room sign-ins or charges; queued work finishes."
                : iface.published ? "Paused: the appliance does not connect to the PMS through this connection."
                  : "Never configured — not connected to any PMS."
        }
      >
        {!writable && !retired ? (
          <p className="text-sm text-muted-foreground">Your role can view this connection but not change it.</p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {moves.map((m) => {
              const blocked = m.to === "ACTIVE" && !iface.published;
              return (
                <Button key={m.to} variant={m.variant} disabled={blocked}
                  onClick={() => { setErr(null); setReason(m.to === "ACTIVE" ? "COMMISSIONING" : "PMS_MAINTENANCE"); setMove(m); }}>
                  {m.label}
                </Button>
              );
            })}
          </div>
        )}
        {writable && iface.lifecycle_state === "AUTH_DISABLED" && !iface.published && (
          <Callout tone="info">
            {drafts.length
              ? "Put the draft configuration live first (on Configuration) — there is nothing to connect to until then."
              : "Configure the connection and put the configuration live first — there is nothing to connect to until then."}
          </Callout>
        )}
        {!retired && (
          <p className="text-xs text-muted-foreground">
            Retiring a connection permanently is not offered here: stays and messages already recorded keep
            referring to it, so it is done as a separate, supported procedure.
          </p>
        )}
      </SheetSection>

      {canTestConnection(provider) && !retired && (
        <SheetSection title="Test the connection"
          description={`Signs in to ${provider.label} with the live configuration and reads a small sample of reservations. Nothing is written to the PMS.`}>
          {writable ? (
            iface.published
              ? <TestConnectionButton iface={iface} provider={provider} revision={null} showResultInline />
              : <p className="text-sm text-muted-foreground">Test a draft from Configuration — nothing is live yet.</p>
          ) : <p className="text-sm text-muted-foreground">Your role cannot run a connection test.</p>}
        </SheetSection>
      )}

      {canFullResync(provider) ? (
        iface.lifecycle_state === "ACTIVE" && writable
          ? <SynchronizationCard id={iface.id} health={health ?? null} onRefreshed={onChanged} />
          : !retired && (
            <SheetSection title="Guest list refresh">
              <p className="text-sm text-muted-foreground">
                {iface.lifecycle_state !== "ACTIVE"
                  ? "Available while the connection is active."
                  : "Your role can view the guest list status but not request a refresh."}
              </p>
            </SheetSection>
          )
      ) : (
        <SheetSection title="Guest list refresh">
          <p className="text-sm text-muted-foreground">
            {provider.label} does not support requesting a full guest list. The appliance keeps it current from the
            provider&rsquo;s regular updates.
          </p>
        </SheetSection>
      )}

      <ConfirmDialog
        open={move !== null}
        onOpenChange={(v) => !v && setMove(null)}
        title={move?.title ?? ""}
        description={move?.description}
        confirmLabel={move?.label ?? "Confirm"}
        confirmVariant={move?.variant === "danger" ? "danger" : "primary"}
        busy={busy}
        error={pmsErrorText(err)}
        requirePassword
        onConfirm={async ({ password }) => {
          if (!move) return;
          setBusy(true); setErr(null);
          try {
            await api.post(`/pms-interfaces/${iface.id}/lifecycle`, { state: move.to, reason_code: reason, password });
            toast.success(
              move.to === "ACTIVE" ? "Connection activated" : move.to === "DRAINING" ? "Connection winding down" : "Room sign-in paused",
              move.to === "ACTIVE" ? "It may take a moment to connect and load the guest list." : undefined,
            );
            setMove(null);
            await onChanged();
          } catch (e) { setErr(e); } finally { setBusy(false); }
        }}
      >
        <Field label="Reason" hint="Recorded with the change.">
          <Select value={reason} onChange={(e) => setReason(e.target.value)}>
            {LIFECYCLE_REASONS.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
          </Select>
        </Field>
      </ConfirmDialog>
    </>
  );
}

const STAGE_WORDS: Record<string, string> = {
  CONFIG: "checking the configuration",
  AUTH: "signing in to the provider",
  READ: "reading reservations",
};

function TestConnectionButton({
  iface, provider, revision, showResultInline = true,
}: {
  iface: PmsConnection;
  provider: PmsProvider;
  revision: PmsRevision | null;
  showResultInline?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [result, setResult] = useState<TestConnectionResult | null>(null);

  return (
    <div className="space-y-2">
      <Button size="sm" variant="secondary" onClick={() => { setErr(null); setOpen(true); }}>
        {revision ? `Test version ${revision.revision_no}` : "Test connection"}
      </Button>
      {showResultInline && result && (
        result.ok ? (
          <Callout tone="success" title="The connection test passed">
            {provider.label} answered
            {typeof result.latency_ms === "number" ? ` in ${result.latency_ms.toLocaleString()} ms` : ""}.
            {typeof result.details?.sample_reservations === "number"
              ? ` ${result.details.sample_reservations.toLocaleString()} reservation${result.details.sample_reservations === 1 ? " was" : "s were"} readable.`
              : ""}
            {result.details?.property ? ` Property: ${result.details.property}.` : ""}
          </Callout>
        ) : (
          <Callout tone="danger" title={`The test failed while ${STAGE_WORDS[result.stage ?? ""] ?? "testing"}`}>
            {result.message || "The provider did not accept the request."}
          </Callout>
        )
      )}
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={revision ? `Test version ${revision.revision_no}?` : "Test the connection?"}
        description={`The appliance signs in to ${provider.label} with ${revision ? `draft version ${revision.revision_no}` : "the live configuration"} and reads a small sample of reservations. Nothing is written to the PMS and nothing changes for guests.`}
        confirmLabel="Run test"
        busy={busy}
        error={pmsErrorText(err)}
        requirePassword
        onConfirm={async ({ password }) => {
          setBusy(true); setErr(null);
          try {
            const body: Record<string, unknown> = { password };
            if (revision) body.revision_id = revision.id;
            const r = await api.post<TestConnectionResult>(`/pms-interfaces/${iface.id}/test-connection`, body);
            setResult(r ?? { ok: false, message: "No answer was returned." });
            setOpen(false);
          } catch (e) { setErr(e); } finally { setBusy(false); }
        }}
      />
    </div>
  );
}
