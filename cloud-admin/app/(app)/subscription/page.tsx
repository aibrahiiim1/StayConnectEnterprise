"use client";

import { useEffect, useState } from "react";
import { api, ApiError, EffectiveLimit, ListResp, Plan, Subscription } from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { Check, ArrowUpRight, ArrowDownRight, Minus } from "lucide-react";
import { formatDate } from "@/lib/utils";

function priceLabel(p: Plan): string {
  const per = p.billing_cycle === "yearly" ? "/yr" : "/mo";
  return `${(p.price_cents / 100).toFixed(0)} ${p.currency}${per}`;
}

export default function SubscriptionPage() {
  const tenantID = useTenant();
  const [sub, setSub] = useState<Subscription | null>(null);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [limits, setLimits] = useState<EffectiveLimit[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [changeResult, setChangeResult] = useState<{ type: string; to: string } | null>(null);

  async function load() {
    if (!tenantID) return;
    try {
      const [s, pl, el] = await Promise.all([
        api.get<Subscription>(`/v1/tenants/${tenantID}/subscription`).catch(() => null),
        api.get<ListResp<Plan>>("/v1/plans"),
        api.get<ListResp<EffectiveLimit>>(`/v1/tenants/${tenantID}/effective-limits`),
      ]);
      setSub(s);
      setPlans(pl.data);
      setLimits(el.data);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onChange(plan: Plan) {
    if (!tenantID) return;
    if (!confirm(`Switch to ${plan.name}? The current subscription will be canceled immediately.`)) return;
    setBusy(plan.id); setErr(null); setChangeResult(null);
    try {
      const r = await api.post<{ change_type: string; to_plan_id: string }>(
        `/v1/tenants/${tenantID}/subscription`,
        { plan_id: plan.id }
      );
      setChangeResult({ type: r.change_type, to: plan.name });
      load();
    } catch (e: any) {
      if (e instanceof ApiError) setErr(e.message);
      else setErr(e?.message ?? "Change failed");
    } finally { setBusy(null); }
  }

  function limitDisplay(l: EffectiveLimit): string {
    if (l.value_type === "bool") return l.bool_value ? "✓" : "—";
    if (l.value_type === "int") {
      if (l.int_value === -1) return "Unlimited";
      return `${l.int_value?.toLocaleString() ?? "—"}${l.unit ? " " + l.unit : ""}`;
    }
    return l.str_value ?? "—";
  }

  const changeIcon = (t: string) =>
    t === "upgrade" ? <ArrowUpRight size={14} /> : t === "downgrade" ? <ArrowDownRight size={14} /> : <Minus size={14} />;

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">Billing</div>
        <h1 className="text-2xl font-semibold">Plan & subscription</h1>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {changeResult && (
        <div className="mb-4 text-sm rounded-md border border-border bg-panel2 px-4 py-2 inline-flex items-center gap-2">
          {changeIcon(changeResult.type)}
          <span className="text-text">Switched to <b>{changeResult.to}</b></span>
          <Badge tone={changeResult.type === "upgrade" ? "ok" : changeResult.type === "downgrade" ? "warn" : "default"}>
            {changeResult.type}
          </Badge>
        </div>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4 mb-6">
        <Card className="lg:col-span-1">
          <CardHeader><CardTitle>Current subscription</CardTitle></CardHeader>
          <CardBody>
            {!sub ? <EmptyState title="No active subscription" /> : (
              <div className="space-y-2 text-sm">
                <div className="flex justify-between">
                  <span className="text-muted">Plan</span>
                  <span className="font-medium">{sub.plan_name}</span>
                </div>
                <div className="flex justify-between">
                  <span className="text-muted">Status</span>
                  <Badge tone={sub.status === "active" ? "ok" : sub.status === "trialing" ? "info" : "warn"}>
                    {sub.status}
                  </Badge>
                </div>
                <div className="flex justify-between">
                  <span className="text-muted">Billing</span>
                  <span>{sub.billing_cycle}</span>
                </div>
                <div className="flex justify-between">
                  <span className="text-muted">Period ends</span>
                  <span>{formatDate(sub.current_period_end)}</span>
                </div>
                {sub.trial_end && (
                  <div className="flex justify-between">
                    <span className="text-muted">Trial ends</span>
                    <span>{formatDate(sub.trial_end)}</span>
                  </div>
                )}
              </div>
            )}
          </CardBody>
        </Card>

        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>Effective limits</CardTitle></CardHeader>
          <CardBody className="p-0">
            <Table>
              <THead><TR><TH>Key</TH><TH>Value</TH><TH>Source</TH></TR></THead>
              <tbody>
                {limits.map((l) => (
                  <TR key={l.key}>
                    <TD className="font-mono text-xs">{l.key}</TD>
                    <TD>{limitDisplay(l)}</TD>
                    <TD>
                      <Badge tone={l.source === "override" ? "warn" : "default"}>{l.source}</Badge>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          </CardBody>
        </Card>
      </div>

      <Card>
        <CardHeader><CardTitle>Available plans</CardTitle></CardHeader>
        <CardBody>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {plans.map((p) => {
              const current = sub?.plan_id === p.id;
              return (
                <div
                  key={p.id}
                  className={`rounded-lg border ${current ? "border-brand bg-[#121a2e]" : "border-border bg-panel2"} p-4 flex flex-col`}
                >
                  <div className="flex items-start justify-between">
                    <div>
                      <div className="text-sm font-semibold">{p.name}</div>
                      <div className="text-xs text-muted font-mono">{p.code}</div>
                    </div>
                    <div className="text-right">
                      <div className="text-lg font-semibold">{priceLabel(p)}</div>
                      {p.trial_days > 0 && <div className="text-xs text-muted">{p.trial_days}d trial</div>}
                    </div>
                  </div>
                  {p.description && <div className="text-xs text-muted mt-2 flex-1">{p.description}</div>}
                  <div className="mt-4">
                    {current ? (
                      <div className="flex items-center gap-2 text-sm text-brand">
                        <Check size={14} /> Current plan
                      </div>
                    ) : (
                      <Button
                        size="sm" variant="secondary"
                        disabled={busy === p.id || !tenantID}
                        onClick={() => onChange(p)}
                        className="w-full"
                      >
                        {busy === p.id ? "Switching…" : "Switch to this plan"}
                      </Button>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
