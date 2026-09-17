"use client";

// Checkout Grace — the hotel's own policy, authored here.
//
// WHAT CHANGED AND WHY IT HAD TO
// ------------------------------
// This screen used to ask the operator to CHOOSE A PACKAGE. That could not work, and on PRE-LIVE it did not:
// the checkout validator demands exact equality between the typed policy and the pinned package revision, so
// the only package that can ever be selected is one built from the policy. With none present the page said
// "publish one through the commercial catalog first" — an instruction no operator could follow, because the
// grace package is a reserved system package the operator publisher refuses to create. The result was a site
// running on the emergency fallback with no route out of it that did not involve SQL.
//
// So the operator now writes the POLICY — duration, speeds, allowance, devices, eligibility — and the server
// derives the service plan revision and package revision that express it exactly, inside the same transaction
// as the publication. The immutable revision model is untouched; it simply stopped being the operator's
// problem. No UUID, no revision number and no catalog step appears anywhere in this flow.
//
// Publishing changes what every departing guest receives, so it keeps a review step, a password step-up, a
// bounded reason and the version the operator was looking at. A concurrent publication is a 409 that RELOADS
// rather than overwrites.

import { useEffect, useState } from "react";
import { api, CheckoutGraceConfig, GraceHistoryEntry, ListResp } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type EffectivePolicy = {
  /** PUBLISHED when the hotel chose this policy; EMERGENCY_FALLBACK when nothing is published. */
  source: "PUBLISHED" | "EMERGENCY_FALLBACK";
  duration_seconds: number;
  down_kbps: number;
  up_kbps: number;
  data_quota_bytes: number;
  device_limit: number;
  device_limit_policy: string;
  eligibility_window_seconds?: number;
  config_version?: number;
  policy_version?: string;
};

type GraceState = {
  published: boolean;
  config_version: number;
  supported_device_policies: string[];
  policy?: CheckoutGraceConfig;
  /** What a guest checking out RIGHT NOW actually receives, whichever policy is in force. */
  effective?: EffectivePolicy;
  emergency_history?: { count: number; last_at?: string };
};

/** The policy an operator authors. Minutes and Mbps and GB, because those are the units a hotel thinks in. */
type Draft = {
  durationMinutes: number;
  downMbps: number;
  upMbps: number;
  allowanceMb: number;
  deviceLimit: number;
  devicePolicy: string;
  eligibilityMinutes: number;
};

const MB = 1024 * 1024;
/** How many versions stay visible before the list collapses. Nothing is removed -- only not rendered yet. */
const HISTORY_PREVIEW = 5;
const fmtBytes = (n: number) =>
  n >= 1 << 30 ? (n / (1 << 30)).toFixed(1) + " GB" : Math.round(n / MB) + " MB";
const fmtDuration = (s: number) => (s % 3600 === 0 ? s / 3600 + " h" : Math.round(s / 60) + " min");
const fmtKbps = (k: number) => (k >= 1000 ? (k / 1000).toFixed(k % 1000 === 0 ? 0 : 1) + " Mbps" : k + " kbps");

/** Where a brand-new hotel policy starts. Deliberately the emergency terms: the safest first draft is the one
 *  already in force, so publishing without thinking changes nothing a guest would notice. */
const FALLBACK_DRAFT: Draft = {
  durationMinutes: 60,
  downMbps: 5,
  upMbps: 2,
  allowanceMb: 500,
  deviceLimit: 1,
  devicePolicy: "REJECT_NEW_DEVICE",
  eligibilityMinutes: 1440,
};

function draftFromEffective(e: EffectivePolicy | null, fallback: Draft): Draft {
  if (!e) return fallback;
  return {
    durationMinutes: Math.max(1, Math.round(e.duration_seconds / 60)),
    downMbps: e.down_kbps / 1000,
    upMbps: e.up_kbps / 1000,
    allowanceMb: Math.max(1, Math.round(e.data_quota_bytes / MB)),
    deviceLimit: Math.max(1, e.device_limit),
    devicePolicy: e.device_limit_policy || "REJECT_NEW_DEVICE",
    eligibilityMinutes: Math.max(1, Math.round((e.eligibility_window_seconds ?? 86400) / 60)),
  };
}

/** The draft in the units the API speaks. One conversion, in one place, so the review panel and the request
 *  cannot disagree about what is being published. */
function draftToPolicy(d: Draft) {
  return {
    grace_duration_seconds: Math.round(d.durationMinutes * 60),
    grace_down_kbps: Math.round(d.downMbps * 1000),
    grace_up_kbps: Math.round(d.upMbps * 1000),
    grace_data_quota_bytes: Math.round(d.allowanceMb) * MB,
    grace_device_limit: Math.round(d.deviceLimit),
    grace_device_limit_policy: d.devicePolicy,
    eligibility_window_seconds: Math.round(d.eligibilityMinutes * 60),
  };
}

export function CheckoutGraceForm({ canWrite = true }: { canWrite?: boolean }) {
  const [loaded, setLoaded] = useState(false);
  const [version, setVersion] = useState(0);
  const [published, setPublished] = useState(false);
  const [effective, setEffective] = useState<EffectivePolicy | null>(null);
  const [emergencyHistory, setEmergencyHistory] = useState<{ count: number; last_at?: string } | null>(null);
  const [history, setHistory] = useState<GraceHistoryEntry[]>([]);
  /** false when the appliance cannot read the publication ledger. "No history" and "I cannot see the
   *  history" are different claims, and showing the first when the second is true would be a lie. */
  const [historyAvailable, setHistoryAvailable] = useState(true);
  /** Which version's full pinned terms are open. One at a time: this is a reference list, not a diff view. */
  const [expanded, setExpanded] = useState<number | null>(null);
  const [showAllHistory, setShowAllHistory] = useState(false);
  const [devicePolicies, setDevicePolicies] = useState<string[]>(["REJECT_NEW_DEVICE"]);

  /** null = not editing. The editor is opened deliberately, so an operator cannot half-type a policy into a
   *  page they opened to read. */
  const [draft, setDraft] = useState<Draft | null>(null);
  const [reviewing, setReviewing] = useState(false);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("HOTEL_ADMIN_UPDATE");
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const state = await api.get<GraceState>("/checkout-grace");
      setVersion(state.config_version);
      setEffective(state.effective ?? null);
      setEmergencyHistory(state.emergency_history ?? null);
      setPublished(state.published);
      if (state.supported_device_policies?.length) setDevicePolicies(state.supported_device_policies);
      // History is layered on and allowed to be missing: a configuration screen that fails because a ledger
      // read failed would be a worse outage than the one it reports.
      try {
        const h = await api.get<ListResp<GraceHistoryEntry> & { available?: boolean }>(
          "/checkout-grace/history",
        );
        setHistory(h.data ?? []);
        setHistoryAvailable(h.available !== false);
      } catch {
        setHistory([]);
        setHistoryAvailable(false);
      }
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load the checkout grace policy");
    } finally {
      setLoaded(true);
    }
  }

  useEffect(() => {
    load();
  }, []);

  function startEditing() {
    setDraft(draftFromEffective(effective, FALLBACK_DRAFT));
    setReviewing(false);
    setErr(null);
    setMsg(null);
  }

  function cancelEditing() {
    setDraft(null);
    setReviewing(false);
    setPassword("");
    setErr(null);
  }

  const set = (k: keyof Draft, v: number | string) =>
    setDraft((d) => (d ? { ...d, [k]: v } : d));

  /** Refused here as well as at the server, so an obviously impossible policy is a sentence naming the field
   *  rather than a round trip. The server remains the authority; this never widens what it accepts. */
  function draftProblem(d: Draft): string | null {
    const p = draftToPolicy(d);
    if (p.grace_duration_seconds < 60 || p.grace_duration_seconds > 604800)
      return "Grace duration must be between 1 minute and 7 days.";
    if (p.grace_down_kbps <= 0 || p.grace_down_kbps > 10000000) return "Download speed is out of range.";
    if (p.grace_up_kbps <= 0 || p.grace_up_kbps > 10000000) return "Upload speed is out of range.";
    if (p.grace_data_quota_bytes <= 0 || p.grace_data_quota_bytes > 1099511627776)
      return "Data allowance must be between 1 MB and 1 TB.";
    if (p.grace_device_limit < 1 || p.grace_device_limit > 1000) return "Device limit must be between 1 and 1000.";
    if (p.eligibility_window_seconds < 60 || p.eligibility_window_seconds > 604800)
      return "Eligibility window must be between 1 minute and 7 days.";
    return null;
  }

  function review(e: React.FormEvent) {
    e.preventDefault();
    if (!draft) return;
    const problem = draftProblem(draft);
    if (problem) {
      setErr(problem);
      return;
    }
    setErr(null);
    setReviewing(true);
  }

  async function publish() {
    if (!draft) return;
    setBusy(true);
    setErr(null);
    setMsg(null);
    try {
      // No package revision is sent. The server derives the plan and package that express this policy exactly,
      // in the same transaction as the publication.
      const r = await api.put<{ config_version: number }>("/checkout-grace", {
        ...draftToPolicy(draft),
        config_version: version,
        expected_config_version: version,
        password,
        reason_code: reason,
      });
      setPassword("");
      setDraft(null);
      setReviewing(false);
      setMsg("Policy published. Version " + r.config_version + " is now in force for future checkouts.");
      // Re-read rather than patch locally: what is in force is the server's answer, and showing a
      // locally-assembled version of it is how a screen starts lying after a partial failure.
      await load();
    } catch (e: any) {
      if (e?.status === 409) {
        setErr("Someone else published a newer policy. The current one has been reloaded — review it and try again.");
        setReviewing(false);
        await load();
      } else if (e?.status === 401) {
        setErr("Password confirmation failed.");
      } else {
        setErr(e?.message ?? "The policy was refused");
      }
    } finally {
      setBusy(false);
    }
  }

  if (!loaded) {
    return (
      <div className="space-y-4">
        <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">Checkout grace</h1>
        <p className="text-sm">Loading…</p>
      </div>
    );
  }

  const preview = draft ? draftToPolicy(draft) : null;

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">Checkout grace</h1>
      <p className="text-sm">
        Guests who still had valid access when the PMS checked them out keep a bounded grace period on these
        terms.
      </p>

      {/* WHAT GUESTS ACTUALLY GET, FIRST AND UNMISSABLE. "Nothing published" is not "nothing happening": a
          guest departing an unconfigured site still receives grace on the built-in emergency terms. */}
      {effective && (
        <Card>
          <CardBody className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-base font-semibold">In force right now</h2>
              {effective.source === "PUBLISHED" ? (
                <span className="rounded bg-success-subtle px-2 py-0.5 text-xs text-success-subtle-foreground">
                  Hotel policy · version {effective.config_version}
                </span>
              ) : (
                <span role="status" className="rounded bg-warn-subtle px-2 py-0.5 text-xs text-warn-subtle-foreground">
                  Emergency fallback · not a policy this hotel chose
                </span>
              )}
            </div>

            {effective.source === "EMERGENCY_FALLBACK" && (
              <p className="text-sm">
                This hotel has not published a checkout grace policy, so departing guests who still had access
                receive the built-in emergency terms below. Nothing is broken and no guest is cut off — but
                these numbers are a safe default, not a decision anyone made for this hotel.
              </p>
            )}

            <dl aria-label="Effective checkout grace" className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
              <dt>Grace duration</dt>
              <dd>{fmtDuration(effective.duration_seconds)}</dd>
              <dt>Download</dt>
              <dd>{fmtKbps(effective.down_kbps)}</dd>
              <dt>Upload</dt>
              <dd>{fmtKbps(effective.up_kbps)}</dd>
              <dt>Data allowance</dt>
              <dd>{fmtBytes(effective.data_quota_bytes)}</dd>
              <dt>Device limit</dt>
              <dd>
                {/* The built-in fallback carries 0, which means "no extra devices" rather than "no limit".
                    Printing a bare 0 reads like the second. A published policy always carries at least 1. */}
                {effective.device_limit === 0 ? "no extra devices" : effective.device_limit}{" "}
                ({effective.device_limit_policy.replace(/_/g, " ").toLowerCase()})
              </dd>
              {effective.eligibility_window_seconds ? (
                <>
                  <dt>Eligibility window</dt>
                  <dd>{fmtDuration(effective.eligibility_window_seconds)}</dd>
                </>
              ) : null}
            </dl>

            {emergencyHistory && emergencyHistory.count > 0 && (
              <p role="status" className="text-sm">
                The emergency fallback has already been used <b>{emergencyHistory.count}</b>{" "}
                {emergencyHistory.count === 1 ? "time" : "times"}
                {emergencyHistory.last_at ? <>, most recently {new Date(emergencyHistory.last_at).toLocaleString()}</> : null}.
                Each one raised a critical alert on the Alerts page.
              </p>
            )}

            {/* Said explicitly, because it is the question an operator asks next and the wrong answer would be
                expensive: publishing does not reach back. */}
            <p className="text-xs text-muted-foreground">
              Publishing applies to future checkouts only. A guest already inside their grace period keeps the
              exact terms they were given at checkout.
            </p>

            {!draft && (
              <div className="pt-1">
                <Button type="button" onClick={startEditing} disabled={!canWrite}>
                  {published ? "Change policy" : "Create hotel policy"}
                </Button>
                {!canWrite && (
                  <span className="ml-2 text-sm">Your role can view this policy but not change it.</span>
                )}
              </div>
            )}
          </CardBody>
        </Card>
      )}

      {err && (
        <p role="alert" className="text-sm text-destructive">
          {err}
        </p>
      )}
      {msg && (
        <p role="status" className="text-sm text-success-subtle-foreground">
          {msg}
        </p>
      )}

      {draft && !reviewing && (
        <Card>
          <CardBody>
            <form className="space-y-4" onSubmit={review} aria-label="Checkout grace policy">
              <h2 className="text-base font-semibold">
                {published ? "New policy version" : "Hotel checkout grace policy"}
              </h2>
              <p className="text-sm text-muted-foreground">
                Set the terms a departing guest receives. Everything the system needs behind this — the service
                plan and package the terms are delivered by — is created for you when you publish.
              </p>

              <div className="grid gap-4 sm:grid-cols-2">
                <label className="block text-sm">
                  Grace duration (minutes)
                  <Input
                    type="number"
                    min={1}
                    max={10080}
                    value={draft.durationMinutes}
                    onChange={(e) => set("durationMinutes", Number(e.target.value))}
                  />
                </label>
                <div className="text-sm">
                  <label className="block">
                    Eligibility window (minutes)
                    <Input
                      type="number"
                      min={1}
                      max={10080}
                      value={draft.eligibilityMinutes}
                      onChange={(e) => set("eligibilityMinutes", Number(e.target.value))}
                    />
                  </label>
                  <span className="text-xs text-muted-foreground">
                    How recently a guest must have had access for grace to apply.
                  </span>
                </div>
                <label className="block text-sm">
                  Download speed (Mbps)
                  <Input
                    type="number"
                    min={0.1}
                    step={0.1}
                    value={draft.downMbps}
                    onChange={(e) => set("downMbps", Number(e.target.value))}
                  />
                </label>
                <label className="block text-sm">
                  Upload speed (Mbps)
                  <Input
                    type="number"
                    min={0.1}
                    step={0.1}
                    value={draft.upMbps}
                    onChange={(e) => set("upMbps", Number(e.target.value))}
                  />
                </label>
                <label className="block text-sm">
                  Data allowance (MB)
                  <Input
                    type="number"
                    min={1}
                    value={draft.allowanceMb}
                    onChange={(e) => set("allowanceMb", Number(e.target.value))}
                  />
                </label>
                <label className="block text-sm">
                  Device limit
                  <Input
                    type="number"
                    min={1}
                    max={1000}
                    value={draft.deviceLimit}
                    onChange={(e) => set("deviceLimit", Number(e.target.value))}
                  />
                </label>
                <div className="text-sm">
                  <label className="block">
                    Device policy
                    <select
                      aria-label="Device policy"
                      className="mt-1 w-full rounded border px-2 py-1"
                      value={draft.devicePolicy}
                      onChange={(e) => set("devicePolicy", e.target.value)}
                    >
                      {devicePolicies.map((p) => (
                        <option key={p} value={p}>
                          {p.replace(/_/g, " ").toLowerCase()}
                        </option>
                      ))}
                    </select>
                  </label>
                  <span className="text-xs text-muted-foreground">
                    Only policies the enforcement path can actually honour are offered.
                  </span>
                </div>
              </div>

              <div className="flex gap-2">
                <Button type="submit">Review before publishing</Button>
                <Button type="button" variant="secondary" onClick={cancelEditing}>
                  Cancel
                </Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {/* THE REVIEW STEP IS NOT DECORATION. The numbers below are the ones that will be sent, produced by the
          same conversion the request uses, so what the operator confirms and what is published cannot differ. */}
      {draft && reviewing && preview && (
        <Card>
          <CardBody className="space-y-3">
            <h2 className="text-base font-semibold">Review and publish</h2>
            <p className="text-sm">
              These are the exact terms a guest will receive at checkout once this is published.
            </p>
            <dl aria-label="Policy to publish" className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
              <dt>Grace duration</dt>
              <dd>{fmtDuration(preview.grace_duration_seconds)}</dd>
              <dt>Download</dt>
              <dd>{fmtKbps(preview.grace_down_kbps)}</dd>
              <dt>Upload</dt>
              <dd>{fmtKbps(preview.grace_up_kbps)}</dd>
              <dt>Data allowance</dt>
              <dd>{fmtBytes(preview.grace_data_quota_bytes)}</dd>
              <dt>Device limit</dt>
              <dd>
                {preview.grace_device_limit} ({preview.grace_device_limit_policy.replace(/_/g, " ").toLowerCase()})
              </dd>
              <dt>Eligibility window</dt>
              <dd>{fmtDuration(preview.eligibility_window_seconds)}</dd>
            </dl>
            <p className="text-xs text-muted-foreground">
              This will become version {version + 1}. Guests already inside their grace period keep the terms
              they were given.
            </p>

            <div className="text-sm">
              <label className="block">
                Reason
                <Input value={reason} onChange={(e) => setReason(e.target.value.toUpperCase())} />
              </label>
              <span className="text-xs text-muted-foreground">
                Recorded against this version in the policy history.
              </span>
            </div>
            <label className="block text-sm">
              Confirm your password
              <Input
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>

            <div className="flex gap-2">
              <Button type="button" onClick={publish} disabled={!canWrite || busy}>
                {busy ? "Publishing…" : "Publish policy"}
              </Button>
              <Button type="button" variant="secondary" onClick={() => setReviewing(false)} disabled={busy}>
                Back
              </Button>
            </div>
          </CardBody>
        </Card>
      )}

      {!historyAvailable && (
        <Card>
          <CardBody>
            <h2 className="text-base font-semibold">Policy history</h2>
            <p role="status" className="text-sm">
              The published-policy record cannot be read on this appliance, so it is not shown. This does not
              mean no policy has been published — the record exists and is append-only; this screen simply
              cannot see it. Nothing about the policy in force above is affected.
            </p>
          </CardBody>
        </Card>
      )}

      {historyAvailable && history.length > 0 && (
        <Card>
          <CardBody className="space-y-3">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <h2 className="text-base font-semibold">Policy history</h2>
              <span className="text-xs text-muted-foreground">
                {history.length} version{history.length === 1 ? "" : "s"}
              </span>
            </div>

            {/* WHY NOTHING HERE DELETES ANYTHING.
                This is the append-only publication ledger: it is the provenance of what every departing guest
                was promised, and the record a rollback is judged against. Hiding a row would make the version
                sequence lie by omission, and deleting one would destroy audit evidence. So the clutter is
                solved where the clutter actually is -- in the rendering. Rows are one line each and open for
                the full pinned terms on demand, and older versions stay collapsed behind a control rather
                than being removed from the record. */}
            <p className="text-sm text-muted-foreground">
              Newest first. Open a version to see the exact terms it put in force. The record is append-only:
              superseding a policy never rewrites, hides or removes what an earlier one promised.
            </p>

            <ul aria-label="Checkout grace policy history" className="divide-y rounded border">
              {(showAllHistory ? history : history.slice(0, HISTORY_PREVIEW)).map((h) => {
                const p = h.policy ?? {};
                const open = expanded === h.config_version;
                return (
                  <li key={h.config_version}>
                    <button
                      type="button"
                      aria-expanded={open}
                      onClick={() => setExpanded(open ? null : h.config_version)}
                      className="flex w-full flex-wrap items-baseline gap-x-3 gap-y-1 px-3 py-2 text-left text-sm hover:bg-muted-surface/40"
                    >
                      <span className="font-medium tabular-nums">v{h.config_version}</span>
                      {h.config_version === version && (
                        <span className="rounded bg-success-subtle px-1.5 py-0.5 text-xs text-success-subtle-foreground">
                          in force
                        </span>
                      )}
                      <span className="text-muted-foreground">
                        {p.grace_duration_seconds ? fmtDuration(p.grace_duration_seconds) : "—"}
                        {p.grace_down_kbps ? ` · ${fmtKbps(p.grace_down_kbps)}` : ""}
                        {p.grace_data_quota_bytes ? ` · ${fmtBytes(p.grace_data_quota_bytes)}` : ""}
                      </span>
                      <span className="ml-auto text-xs text-muted-foreground">
                        {new Date(h.published_at).toLocaleDateString()} · {h.actor}
                      </span>
                    </button>

                    {open && (
                      <dl
                        aria-label={`Version ${h.config_version} details`}
                        className="grid grid-cols-2 gap-x-4 gap-y-1 border-t bg-muted-surface/20 px-3 py-2 text-sm"
                      >
                        <dt>Published</dt>
                        <dd>{new Date(h.published_at).toLocaleString()}</dd>
                        <dt>Published by</dt>
                        <dd>{h.actor}</dd>
                        <dt>Reason</dt>
                        <dd>{h.reason_code || "—"}</dd>
                        <dt>Grace duration</dt>
                        <dd>{p.grace_duration_seconds ? fmtDuration(p.grace_duration_seconds) : "—"}</dd>
                        <dt>Download</dt>
                        <dd>{p.grace_down_kbps ? fmtKbps(p.grace_down_kbps) : "—"}</dd>
                        <dt>Upload</dt>
                        <dd>{p.grace_up_kbps ? fmtKbps(p.grace_up_kbps) : "—"}</dd>
                        <dt>Data allowance</dt>
                        <dd>{p.grace_data_quota_bytes ? fmtBytes(p.grace_data_quota_bytes) : "—"}</dd>
                        <dt>Device limit</dt>
                        <dd>
                          {p.grace_device_limit ?? "—"}
                          {p.grace_device_limit_policy
                            ? ` (${p.grace_device_limit_policy.replace(/_/g, " ").toLowerCase()})`
                            : ""}
                        </dd>
                        <dt>Eligibility window</dt>
                        <dd>
                          {p.eligibility_window_seconds ? fmtDuration(p.eligibility_window_seconds) : "—"}
                        </dd>
                      </dl>
                    )}
                  </li>
                );
              })}
            </ul>

            {history.length > HISTORY_PREVIEW && (
              <Button type="button" variant="secondary" onClick={() => setShowAllHistory((v) => !v)}>
                {showAllHistory
                  ? `Show recent ${HISTORY_PREVIEW} only`
                  : `Show all ${history.length} versions`}
              </Button>
            )}
          </CardBody>
        </Card>
      )}
    </div>
  );
}
