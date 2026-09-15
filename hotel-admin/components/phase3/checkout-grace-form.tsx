"use client";

// Phase 3 (DARK) — Checkout-grace policy.
//
// The operator CHOOSES A PACKAGE; they do not type numbers and they never paste a UUID. Every scalar shown is
// read from the pinned immutable Package/Plan revision, so what is published and what the package can actually
// deliver agree by construction rather than by careful data entry. The server validates the same equality with
// the same function the Checkout conversion uses, so a policy that "saved" can never be the one that silently
// falls back to Emergency Grace on the next departure.
//
// Publishing changes what every departing guest receives, so it carries a password step-up, a bounded reason,
// and the version the operator was looking at. A concurrent publication is a 409 that RELOADS rather than
// overwrites.

import { useEffect, useState } from "react";
import { api, CheckoutGraceConfig, GracePackageOption, ListResp } from "@/lib/api";
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

const fmtBytes = (n: number) =>
  n >= 1 << 30 ? (n / (1 << 30)).toFixed(1) + " GB" : Math.round(n / (1 << 20)) + " MB";
const fmtDuration = (s: number) => (s % 3600 === 0 ? s / 3600 + " h" : Math.round(s / 60) + " min");

export function CheckoutGraceForm({ canWrite = true }: { canWrite?: boolean }) {
  const [packages, setPackages] = useState<GracePackageOption[] | null>(null);
  const [selected, setSelected] = useState<string>("");
  const [version, setVersion] = useState(0);
  const [published, setPublished] = useState(false);
  const [eligibility, setEligibility] = useState(86400);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("POLICY_UPDATE");
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [effective, setEffective] = useState<EffectivePolicy | null>(null);
  const [emergencyHistory, setEmergencyHistory] = useState<{ count: number; last_at?: string } | null>(null);

  async function load() {
    try {
      const [state, pkgs] = await Promise.all([
        api.get<GraceState>("/checkout-grace"),
        api.get<ListResp<GracePackageOption>>("/checkout-grace/packages"),
      ]);
      setVersion(state.config_version);
      setEffective(state.effective ?? null);
      setEmergencyHistory(state.emergency_history ?? null);
      setPublished(state.published);
      if (state.policy) setEligibility(state.policy.eligibility_window_seconds);
      setPackages(pkgs.data);
      const current = pkgs.data.find((p) => p.selected);
      setSelected(current?.package_revision_id ?? "");
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load the checkout-grace policy");
      setPackages([]);
    }
  }

  useEffect(() => {
    load();
  }, []);

  const chosen = packages?.find((p) => p.package_revision_id === selected);

  async function publish(e: React.FormEvent) {
    e.preventDefault();
    if (!chosen) return;
    setBusy(true);
    setErr(null);
    setMsg(null);
    try {
      // Every scalar comes from the pinned revision, so the form cannot express a policy the package could
      // not deliver.
      const r = await api.put<{ config_version: number }>("/checkout-grace", {
        grace_package_revision_id: chosen.package_revision_id,
        grace_duration_seconds: chosen.grace_duration_seconds,
        grace_down_kbps: chosen.down_kbps,
        grace_up_kbps: chosen.up_kbps,
        grace_data_quota_bytes: chosen.data_quota_bytes,
        grace_device_limit: chosen.device_limit,
        grace_device_limit_policy: chosen.device_limit_policy,
        eligibility_window_seconds: eligibility,
        config_version: version,
        expected_config_version: version,
        password,
        reason_code: reason,
      });
      setVersion(r.config_version);
      setPublished(true);
      setPassword("");
      setMsg("Policy published (version " + r.config_version + ").");
    } catch (e: any) {
      if (e?.status === 409) {
        setErr("Someone else published a newer policy. The current one has been reloaded — review it and try again.");
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

  if (packages === null) {
    return (
      <div className="space-y-4">
        <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">Checkout grace</h1>
        <p className="text-sm">Loading…</p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">Checkout grace</h1>
      <p className="text-sm">
        Guests who had valid access at checkout keep a bounded grace period, delivered by the package you choose
        here. Currently version {version}
        {published ? "" : " (nothing published yet)"}.
      </p>

      {/* WHAT GUESTS ACTUALLY GET, FIRST AND UNMISSABLE.
          The screen used to say only "(nothing published yet)", which reads as "nothing is happening". It is
          not: a guest who checks out of an unconfigured site still receives a grace -- the built-in Emergency
          fallback -- and an operator had no way to learn what that was or that it had already been used. This
          panel always answers the question the page exists to answer. */}
      {effective && (
        <Card>
          <CardBody className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-base font-semibold">In force right now</h2>
              {effective.source === "PUBLISHED" ? (
                <span className="rounded bg-success-subtle px-2 py-0.5 text-xs text-success-subtle-foreground">
                  Published policy · version {effective.config_version}
                </span>
              ) : (
                <span role="status" className="rounded bg-warn-subtle px-2 py-0.5 text-xs text-warn-subtle-foreground">
                  Emergency fallback · not a policy this hotel chose
                </span>
              )}
            </div>

            {effective.source === "EMERGENCY_FALLBACK" && (
              <p className="text-sm">
                No checkout-grace policy has been published for this site, so departing guests who still had
                access receive the built-in emergency terms below. Nothing is broken and no guest is cut off —
                but these numbers are a safe default, not a decision anyone made for this hotel. Publish a
                policy below to replace them.
              </p>
            )}

            <dl aria-label="Effective checkout grace" className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
              <dt>Grace duration</dt>
              <dd>{fmtDuration(effective.duration_seconds)}</dd>
              <dt>Download</dt>
              <dd>{effective.down_kbps} kbps</dd>
              <dt>Upload</dt>
              <dd>{effective.up_kbps} kbps</dd>
              <dt>Data allowance</dt>
              <dd>{fmtBytes(effective.data_quota_bytes)}</dd>
              <dt>Device limit</dt>
              <dd>
                {effective.device_limit === 0 ? "no extra devices" : effective.device_limit}{" "}
                ({effective.device_limit_policy.replace(/_/g, " ").toLowerCase()})
              </dd>
            </dl>

            {emergencyHistory && emergencyHistory.count > 0 && (
              <p role="status" className="text-sm">
                The emergency fallback has already been used <b>{emergencyHistory.count}</b>{" "}
                {emergencyHistory.count === 1 ? "time" : "times"}
                {emergencyHistory.last_at ? <>, most recently {new Date(emergencyHistory.last_at).toLocaleString()}</> : null}.
                Each one raised a critical alert on the Alerts page.
              </p>
            )}

            {/* SAID EXPLICITLY, because it is the question an operator asks next and the wrong answer would be
                expensive: publishing does not reach back. */}
            <p className="text-xs text-muted-foreground">
              Publishing a policy applies to future checkouts only. A guest already inside their grace period
              keeps the exact terms they were given at checkout.
            </p>
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

      <Card>
        <CardBody>
          {packages.length === 0 ? (
            <p role="alert" className="text-sm">
              No checkout-grace package is available for this site yet. Publish one through the commercial
              catalog first — a grace policy without a package would fall back to emergency grace on every
              checkout.
            </p>
          ) : (
            <form className="space-y-4" onSubmit={publish}>
              <label className="block text-sm">
                Grace package
                <select
                  aria-label="Grace package"
                  className="mt-1 w-full rounded border px-2 py-1"
                  value={selected}
                  onChange={(e) => setSelected(e.target.value)}
                >
                  <option value="">— select a package —</option>
                  {packages.map((p) => (
                    <option key={p.package_revision_id} value={p.package_revision_id}>
                      {p.package_code} · revision {p.revision_no} · {p.service_plan_code}
                    </option>
                  ))}
                </select>
              </label>

              {chosen && (
                // Read-only: these ARE the pinned revision's values. Changing the policy means publishing a
                // new immutable package revision through the catalog, not editing numbers here.
                <dl aria-label="Selected package policy" className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
                  <dt>Download</dt>
                  <dd>{chosen.down_kbps} kbps</dd>
                  <dt>Upload</dt>
                  <dd>{chosen.up_kbps} kbps</dd>
                  <dt>Data allowance</dt>
                  <dd>{fmtBytes(chosen.data_quota_bytes)}</dd>
                  <dt>Grace duration</dt>
                  <dd>{fmtDuration(chosen.grace_duration_seconds)}</dd>
                  <dt>Device limit</dt>
                  <dd>
                    {chosen.device_limit} ({chosen.device_limit_policy.replace(/_/g, " ").toLowerCase()})
                  </dd>
                  <dt>Settlement</dt>
                  <dd>{chosen.settlement_mode.replace(/_/g, " ").toLowerCase()}</dd>
                  <dt>Plan revision</dt>
                  <dd>
                    {chosen.service_plan_code} · revision {chosen.service_plan_revision_no}
                  </dd>
                  <dt>Time accounting</dt>
                  <dd>{chosen.time_accounting_mode.replace(/_/g, " ").toLowerCase()}</dd>
                  <dt>End mode</dt>
                  <dd>{chosen.end_mode.replace(/_/g, " ").toLowerCase()}</dd>
                  <dt>Policy version</dt>
                  <dd>{chosen.policy_version}</dd>
                  <dt>Status</dt>
                  <dd>
                    {chosen.is_current ? "current revision" : "superseded"}
                    {chosen.is_active ? "" : " · package inactive"}
                  </dd>
                </dl>
              )}

              <label className="block text-sm">
                Eligibility window (seconds)
                <Input
                  type="number"
                  min={1}
                  value={eligibility}
                  onChange={(e) => setEligibility(Number(e.target.value))}
                />
              </label>
              <label className="block text-sm">
                Reason
                <Input value={reason} onChange={(e) => setReason(e.target.value.toUpperCase())} />
              </label>
              <label className="block text-sm">
                Confirm your password
                <Input
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </label>

              <Button type="submit" disabled={!canWrite || busy || !chosen}>
                {busy ? "Publishing…" : "Publish policy"}
              </Button>
              {!canWrite && <span className="ml-2 text-sm">Your role can view this policy but not change it.</span>}
            </form>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
