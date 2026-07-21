"use client";

// Phase 3 (DARK) — Checkout-grace policy.
//
// Publishing changes what every departing guest receives, so it is treated like the other privileged
// operations on this appliance: the whole typed policy is published together, the operator confirms with
// their password, gives a bounded reason, and sends the version they were looking at. If someone else
// published in the meantime the server answers 409 and this page RELOADS rather than overwriting their work.

import { useEffect, useState } from "react";
import { api, CheckoutGraceConfig } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

const DEFAULTS: CheckoutGraceConfig = {
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

type GraceState = {
  published: boolean;
  config_version: number;
  supported_device_policies: string[];
  policy?: CheckoutGraceConfig;
};

export function CheckoutGraceForm({ canWrite = true }: { canWrite?: boolean }) {
  const [cfg, setCfg] = useState<CheckoutGraceConfig | null>(null);
  const [version, setVersion] = useState(0);
  const [policies, setPolicies] = useState<string[]>(["REJECT_NEW_DEVICE"]);
  const [password, setPassword] = useState("");
  const [reason, setReason] = useState("POLICY_UPDATE");
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const s = await api.get<GraceState>("/checkout-grace");
      setVersion(s.config_version);
      setPolicies(s.supported_device_policies ?? ["REJECT_NEW_DEVICE"]);
      setCfg(s.policy ?? { ...DEFAULTS });
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load the checkout-grace policy");
      setCfg({ ...DEFAULTS });
    }
  }

  useEffect(() => {
    load();
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
      const r = await api.put<{ config_version: number }>("/checkout-grace", {
        ...cfg,
        expected_config_version: version, // what this operator was looking at
        password,
        reason_code: reason,
      });
      setVersion(r.config_version);
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

  if (!cfg) {
    return (
      <div className="space-y-4">
        <h1 className="text-xl font-semibold">Checkout grace</h1>
        <p className="text-sm">Loading…</p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Checkout grace</h1>
      <p className="text-sm">
        Guests who had valid access at checkout keep a bounded grace period. The whole policy is published
        together and takes effect as one version — currently version {version}
        {version === 0 ? " (nothing published yet)" : ""}.
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
                {/* only what the enforcement path can actually honour is offered */}
                {policies.map((p) => (
                  <option key={p} value={p}>
                    {p.replace(/_/g, " ").toLowerCase()}
                  </option>
                ))}
              </select>
            </label>
            <label className="text-sm">
              Reason
              <Input value={reason} onChange={(e) => setReason(e.target.value.toUpperCase())} />
            </label>
            <label className="text-sm sm:col-span-2">
              Confirm your password
              <Input
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
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
