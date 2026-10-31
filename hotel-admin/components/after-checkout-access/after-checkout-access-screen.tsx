"use client";

// The one place to configure what a guest keeps after a verified check-out. Contract:
// docs/architecture/ONEGATE_AFTER_CHECKOUT_ACCESS.md.

import * as React from "react";
import { History, Pencil, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import {
  type AccessHistoryItem,
  type AccessHistoryResp,
  type AccessPolicy,
  type AccessState,
  accessWarnings,
  boundsOf,
  devicePolicyTitle,
  fmtData,
  fmtHours,
  fmtSpeed,
  fmtWhen,
  guestReceivesSentence,
  sourceWords,
} from "@/lib/api/after-checkout-access";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { SectionCard } from "@/components/ui/card";
import { DescriptionList, MetricStrip } from "@/components/ui/data";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { formatRelative } from "@/lib/utils";
import { AccessEditorSheet } from "./access-editor-sheet";
import { AccessHistory } from "./access-history";
import { PostStayView } from "@/components/phase5/post-stay-view";

export function AfterCheckoutAccessScreen({ canWrite = true, canActOnReconnections = false, rolesKnown = true }: {
  canWrite?: boolean;
  canActOnReconnections?: boolean;
  rolesKnown?: boolean;
}) {
  const toast = useToast();
  const [state, setState] = React.useState<AccessState | null>(null);
  const [loadError, setLoadError] = React.useState<string | null>(null);
  const [history, setHistory] = React.useState<AccessHistoryItem[]>([]);
  const [historyAvailable, setHistoryAvailable] = React.useState(true);
  const [editing, setEditing] = React.useState(false);
  const [justSaved, setJustSaved] = React.useState<number | null>(null);

  const load = React.useCallback(async () => {
    try {
      const s = await api.get<AccessState>("/after-checkout-access");
      setState(s);
      setLoadError(null);
      // Keep the setting visible when the history ledger cannot be read.
      try {
        const h = await api.get<AccessHistoryResp>("/after-checkout-access/history");
        setHistory(h?.data ?? []);
        setHistoryAvailable(h?.available !== false);
      } catch {
        setHistory([]);
        setHistoryAvailable(false);
      }
    } catch (e: any) {
      setLoadError(
        e?.status === 404
          ? "After Check-out Access is not available on this appliance, so there is nothing to show."
          : `After Check-out Access could not be loaded${e?.message ? `: ${e.message}` : "."}`,
      );
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  const source = state?.effective?.source;
  const words = sourceWords(source);
  const effectivePolicy: AccessPolicy | undefined = state?.effective?.policy;
  const version = state?.config_version ?? 0;
  const latest = history.find((h) => h.config_version === version);
  const bounds = boundsOf(state);
  const supported = state?.supported_device_policies?.length ? state.supported_device_policies : ["REJECT_NEW_DEVICE"];
  const warnings = state ? accessWarnings(state, historyAvailable) : [];
  const used = state?.emergency_history?.count ?? 0;
  const lastUsed = state?.emergency_history?.last_at;

  async function onSaved(newVersion: number) {
    setEditing(false);
    setJustSaved(newVersion);
    toast.success("After Check-out Access saved", `Version ${newVersion} applies to future check-outs.`);
    await load();
  }

  return (
    <PageShell>
      <PageHeader
        title="After Check-out Access"
        description="What a guest's devices keep, for how long, after a verified check-out."
        help={
          <>
            <HelpSection title="What it does">
              <p>
                After a verified check-out, the devices the guest used on the stay can reconnect for a limited time.
                This is the only place that sets that time, the speeds, the data and the device allowance.
              </p>
            </HelpSection>
            <HelpSection title="The rules">
              <HelpList items={[
                "One shared allowance: all the devices that were authorised on the stay draw on the same data quota.",
                "To reconnect, the guest types only the room number and the reservation number. There is no surname step and no package to choose.",
                "New devices are refused: a device that was not already authorised on that stay cannot join.",
                "The time starts at the verified check-out, not when the guest reconnects.",
                "Changes apply to future check-outs only. A guest who has already checked out keeps the terms they were given.",
              ]} />
            </HelpSection>
            <HelpSection title="When it is off or unset">
              <p>
                Turned off, nobody is given access at check-out. Turned on with nothing published, departing guests get
                the built-in emergency terms (a safe default not chosen for this site) and each use raises a critical alert.
              </p>
            </HelpSection>
            <HelpSection title="Versions and history">
              <p>
                Every change, including turning the feature on or off, is a version with who made it and why. The record
                is append-only.
              </p>
            </HelpSection>
          </>
        }
        actions={
          canWrite ? (
            <Button onClick={() => setEditing(true)} disabled={!state}>
              <Pencil /> Edit settings
            </Button>
          ) : undefined
        }
      />

      {!canWrite && state && <ReadOnlyNotice>Your role can view these settings but not change them.</ReadOnlyNotice>}

      {loadError && (
        <div className="space-y-2">
          <ErrorBanner err={loadError} className="mb-0" />
          <Button variant="secondary" size="sm" onClick={() => void load()}>
            <RefreshCw /> Try again
          </Button>
        </div>
      )}

      {!state && !loadError && (
        <div className="space-y-5" aria-busy="true">
          <span className="sr-only">Loading After Check-out Access</span>
          <StatGrid cols={4}>
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-[5.5rem]" />
            ))}
          </StatGrid>
          <Skeleton className="h-64" />
        </div>
      )}

      {state && (
        <>
          {justSaved !== null && (
            <Callout tone="success" title={`Version ${justSaved} saved`}>
              It applies to future check-outs. Guests who have already checked out keep the terms they were given.
            </Callout>
          )}

          <StatGrid cols={4}>
            <StatCard
              label="Access after check-out"
              value={state.enabled ? "On" : "Off"}
              tone={state.enabled ? "ok" : "default"}
              hint={state.enabled ? "Departing guests are given access" : "Nobody is given access"}
            />
            <StatCard
              label="Setting in force"
              value={words.label}
              tone={words.tone === "warn" ? "warn" : words.tone === "ok" ? "ok" : "default"}
              hint={words.hint}
            />
            <StatCard
              label="Last changed"
              value={
                latest ? (
                  <time dateTime={latest.published_at} title={fmtWhen(latest.published_at)}>{formatRelative(latest.published_at)}</time>
                ) : version > 0 ? "Unknown" : "Never"
              }
              hint={latest ? `by ${latest.actor}` : !historyAvailable ? "History cannot be read" : version > 0 ? "Not in the record" : "Nothing saved yet"}
            />
            <StatCard
              label="Emergency fallback used"
              value={used}
              tone={used > 0 ? "warn" : "default"}
              hint={used > 0 && lastUsed ? <>Last <span title={fmtWhen(lastUsed)}>{formatRelative(lastUsed)}</span> · critical alert</> : "Never used on this appliance"}
              href={used > 0 ? "/operational-alerts" : undefined}
            />
          </StatGrid>

          {warnings.length > 0 && (
            <section aria-label="Needs attention" className="space-y-2">
              {warnings.map((w) => (
                <Callout key={w.id} tone={w.tone} title={w.title}>
                  <p>{w.body}</p>
                </Callout>
              ))}
            </section>
          )}

          <SectionCard
            title="What a departing guest receives"
            description={source === "DISABLED" ? "Turned off." : source === "EMERGENCY_FALLBACK" ? "The built-in emergency terms." : `Site setting, version ${version}.`}
            actions={<span data-testid="aca-source" data-source={source}><Badge tone={words.tone === "ok" ? "ok" : words.tone === "warn" ? "warn" : "neutral"} dot>{words.label}</Badge></span>}
            bodyClassName="space-y-5"
          >
            {effectivePolicy ? (
              <>
                <p className="text-sm leading-relaxed text-foreground sm:text-base" data-testid="aca-sentence">
                  {guestReceivesSentence(effectivePolicy)}
                </p>
                <section aria-label="Allowance">
                  <MetricStrip
                    items={[
                      { label: "Duration after check-out", value: fmtHours(effectivePolicy.duration_hours) },
                      { label: "Download", value: fmtSpeed(effectivePolicy.down_kbps) },
                      { label: "Upload", value: fmtSpeed(effectivePolicy.up_kbps) },
                      { label: "Shared data quota", value: fmtData(effectivePolicy.data_quota_bytes) },
                    ]}
                  />
                </section>
              </>
            ) : (
              <p className="text-sm text-muted-foreground" data-testid="aca-sentence">
                Nothing. After Check-out Access is turned off, so no device is given access at check-out.
              </p>
            )}
            <section aria-label="How it works">
              <DescriptionList
                items={[
                  ...(effectivePolicy
                    ? [{
                        label: "Device allowance",
                        value: `Up to ${effectivePolicy.device_limit} device${effectivePolicy.device_limit === 1 ? "" : "s"} at once`,
                        hint: devicePolicyTitle(effectivePolicy.device_policy),
                      }]
                    : []),
                  { label: "Who can reconnect", value: "Devices authorised on the same stay before check-out", hint: state.eligibility },
                  { label: "How they reconnect", value: "Room number and reservation number", hint: state.sign_in },
                  { label: "The data quota is", value: "Shared by all of those devices", hint: "Not per device: one total for the stay." },
                  { label: "The time starts at", value: "The verified check-out", hint: "Not when the guest reconnects." },
                  { label: "Changes apply to", value: "Future check-outs only", hint: "A guest who has already checked out keeps the terms they were given." },
                ]}
              />
            </section>
          </SectionCard>

          <SectionCard
            flush
            title="History"
            description="Every saved version, including turning the feature on or off. Newest first."
            actions={historyAvailable && history.length > 0 ? <Badge tone="neutral">{history.length} version{history.length === 1 ? "" : "s"}</Badge> : undefined}
          >
            {!historyAvailable ? (
              <EmptyState size="sm" icon={<History />} title="History cannot be read on this appliance"
                hint="The record exists, but this appliance's admin service cannot read it." />
            ) : history.length === 0 ? (
              <EmptyState size="sm" icon={<History />} title="Nothing saved yet"
                hint={canWrite ? "Save the site's settings to replace the emergency fallback. Each version is recorded here." : "When a manager saves the site's settings, each version is recorded here."} />
            ) : (
              <AccessHistory history={history} inForce={version} />
            )}
          </SectionCard>

          <AccessEditorSheet
            open={editing}
            onOpenChange={setEditing}
            state={state}
            bounds={bounds}
            supportedPolicies={supported}
            canWrite={canWrite}
            onSaved={onSaved}
            reload={load}
          />
        </>
      )}

      <PostStayView embedded canAct={canActOnReconnections} rolesKnown={rolesKnown} />
    </PageShell>
  );
}
