"use client";

// Phase 3 (DARK) — Checkout-grace policy. The whole typed policy is published together through the one
// approved writer; the database bumps the version by exactly one and an identical re-publish changes nothing.
// The form therefore submits the COMPLETE policy every time rather than a patch, so what an operator sees is
// exactly what will be in force.

import { useEffect, useState } from "react";
import { api, CheckoutGraceConfig } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

const POLICIES = ["REJECT_NEW_DEVICE", "DISCONNECT_OLDEST", "ADMIN_APPROVAL"];

const EMPTY: CheckoutGraceConfig = {
  grace_package_revision_id: null,
  grace_duration_seconds: 3600,
  grace_down_kbps: 4000,
  grace_up_kbps: 1500,
  grace_data_quota_bytes: 524288000,
  grace_device_limit: 2,
  grace_device_limit_policy: "REJECT_NEW_DEVICE",
  eligibility_window_seconds: 86400,
  config_version: 0,
};

export default function CheckoutGracePage({ canWrite = true }: { canWrite?: boolean }) {
  const [cfg, setCfg] = useState<CheckoutGraceConfig | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        setCfg(await api.get<CheckoutGraceConfig>("/checkout-grace"));
      } catch (e: any) {
        // a site with no published policy yet is normal, not an error state
        if (e?.status === 404) setCfg(EMPTY);
        else setErr(e?.message ?? "Failed to load the checkout-grace policy");
      }
    })();
  }, []);

  function num(field: keyof CheckoutGraceConfig) {
    return (e: React.ChangeEvent<HTMLInputElement>) =>
      setCfg((c) => (c ? { ...c, [field]: Number(e.target.value) } : c));
  }

  async function publish(e: React.FormEvent) {
    e.preventDefault();
    if (!cfg) return;
    setBusy(true);
    setErr(null);
    setMsg(null);
    try {
      const r = await api.put<{ config_version: number }>("/checkout-grace", cfg);
      setCfg({ ...cfg, config_version: r.config_version });
      setMsg("Policy published (version " + r.config_version + ").");
    } catch (e: any) {
      setErr(e?.message ?? "The policy was refused");
    } finally {
      setBusy(false);
    }
  }

  if (!cfg) {
    return (
      <div className="space-y-4">
        <h1 className="text-xl font-semibold">Checkout grace</h1>
        {err ? (
          <p role="alert" className="text-sm text-red-600">
            {err}
          </p>
        ) : (
          <p className="text-sm">Loading…</p>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Checkout grace</h1>
      <p className="text-sm">
        Guests who had valid access at checkout keep a bounded grace period. The whole policy is published
        together and takes effect as one version — version {cfg.config_version}.
      </p>

      {err && (
        <p role="alert" className="text-sm text-red-600">
          {err}
        </p>
      )}
      {msg && (
        <p role="status" className="text-sm text-green-700">
          {msg}
        </p>
      )}

      <Card>
        <CardBody>
          <form className="grid gap-3 sm:grid-cols-2" onSubmit={publish}>
            <label className="text-sm">
              Grace duration (seconds)
              <Input type="number" min={1} value={cfg.grace_duration_seconds} onChange={num("grace_duration_seconds")} />
            </label>
            <label className="text-sm">
              Eligibility window (seconds)
              <Input type="number" min={1} value={cfg.eligibility_window_seconds} onChange={num("eligibility_window_seconds")} />
            </label>
            <label className="text-sm">
              Download (kbps)
              <Input type="number" min={1} value={cfg.grace_down_kbps} onChange={num("grace_down_kbps")} />
            </label>
            <label className="text-sm">
              Upload (kbps)
              <Input type="number" min={1} value={cfg.grace_up_kbps} onChange={num("grace_up_kbps")} />
            </label>
            <label className="text-sm">
              Data allowance (bytes)
              <Input type="number" min={0} value={cfg.grace_data_quota_bytes} onChange={num("grace_data_quota_bytes")} />
            </label>
            <label className="text-sm">
              Device limit
              <Input type="number" min={1} value={cfg.grace_device_limit} onChange={num("grace_device_limit")} />
            </label>
            <label className="text-sm">
              Device limit policy
              <select
                aria-label="Device limit policy"
                className="mt-1 w-full rounded border px-2 py-1"
                value={cfg.grace_device_limit_policy}
                onChange={(e) => setCfg({ ...cfg, grace_device_limit_policy: e.target.value })}
              >
                {POLICIES.map((p) => (
                  <option key={p} value={p}>
                    {p.replace(/_/g, " ").toLowerCase()}
                  </option>
                ))}
              </select>
            </label>
            <div className="sm:col-span-2">
              <Button type="submit" disabled={!canWrite || busy}>
                {busy ? "Publishing…" : "Publish policy"}
              </Button>
              {!canWrite && <span className="ml-2 text-sm">Your role can view this policy but not change it.</span>}
            </div>
          </form>
        </CardBody>
      </Card>
    </div>
  );
}
