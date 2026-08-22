"use client";

// INTERNET PACKAGES — what a guest is offered and can take.
//
// This was "Commercial packages", a four-tab screen holding packages, service plans, checkout grace and a
// quotes/purchases inspector. Three of those are separate jobs done by different people at different times,
// and burying them as tabs meant an operator looking for "what speed does Gold give" had to know that plans
// lived inside packages. Service plans and Checkout grace are now their own pages in the same nav section;
// what remains here is the package itself plus the read-only record of what guests took.
//
// edged is the authority: its routes return 503 while the capability is off, and the page renders a plain
// "not switched on" state rather than an error.

import { Fragment, useCallback, useEffect, useState } from "react";
import { api, ApiError, ListResp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { PublishPackageForm } from "./publish-form";
import type { PublishPayload } from "@/lib/commerce-form";

type PackageSummary = { package_id: string; code: string; active: boolean; current_revision_id: string; revision_count: number };
// Only the fields the plan SELECTOR needs; /service-plans owns the full shape.
type PlanSummary = {
  plan_id: string; code: string; current_revision_id: string;
  name?: string | null; down_kbps?: number | null; max_concurrent_devices?: number | null;
};
type RevisionInfo = { revision_id: string; revision_no: number; is_current: boolean; label?: string; price_minor?: number; currency?: string; package_type?: string };
type QuoteInspect = { id: string; package_revision_id: string; price_minor: number; currency: string; expires_at: string; consumed_at: string | null };
type PurchaseInspect = { id: string; package_revision_id: string; state: string; amount_minor: number; currency: string };

type Tab = "packages" | "inspection";

function useDisabled() {
  const [disabled, setDisabled] = useState(false);
  const guard = useCallback((e: unknown): boolean => {
    if (e instanceof ApiError && e.status === 503) { setDisabled(true); return true; }
    return false;
  }, []);
  return { disabled, setDisabled, guard };
}

export default function CommercialPackagesPage() {
  const [tab, setTab] = useState<Tab>("packages");
  const [err, setErr] = useState<string | null>(null);
  const { disabled, guard } = useDisabled();

  const tabs: { id: Tab; label: string }[] = [
    { id: "packages", label: "Packages" },
    { id: "inspection", label: "Guest activity" },
  ];

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-lg font-semibold">Internet packages</h1>
        <p className="text-sm text-muted mt-1 max-w-2xl">
          An internet package is what a guest sees and takes on the portal. Each one uses a{" "}
          <a href="/service-plans" className="underline">service plan</a>, which defines the speed, device
          count and duration behind it.
        </p>
      </div>
      {disabled ? (
        <Card><CardBody>
          <EmptyState title="The internet offering is not switched on for this appliance"
            hint="Internet packages and service plans become available once this capability is enabled for the site. Contact your StayConnect administrator." />
        </CardBody></Card>
      ) : (
        <>
          <div className="flex gap-2 border-b border-border">
            {tabs.map((t) => (
              <button key={t.id} onClick={() => setTab(t.id)}
                className={`px-3 py-2 text-sm border-b-2 ${tab === t.id ? "border-brand text-text" : "border-transparent text-muted hover:text-text"}`}>
                {t.label}
              </button>
            ))}
          </div>
          {err && <div className="text-sm text-red-500">{err}</div>}
          {tab === "packages" && <PackagesTab guard={guard} setErr={setErr} />}
          {tab === "inspection" && <InspectionTab guard={guard} setErr={setErr} />}
        </>
      )}
    </div>
  );
}

type TabProps = { guard: (e: unknown) => boolean; setErr: (s: string | null) => void };

function PackagesTab({ guard, setErr }: TabProps) {
  const [rows, setRows] = useState<PackageSummary[] | null>(null);
  const [plans, setPlans] = useState<PlanSummary[]>([]);
  const [revs, setRevs] = useState<Record<string, RevisionInfo[]>>({});
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [pk, pl] = await Promise.all([
        api.get<ListResp<PackageSummary>>("/commercial-packages"),
        api.get<ListResp<PlanSummary>>("/commercial-packages/plans"),
      ]);
      setRows(pk.data ?? []); setPlans(pl.data ?? []);
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Failed to load"); }
  }, [guard, setErr]);
  useEffect(() => { load(); }, [load]);

  async function toggleRevs(id: string) {
    if (revs[id]) { setRevs((r) => { const n = { ...r }; delete n[id]; return n; }); return; }
    try {
      const r = await api.get<ListResp<RevisionInfo>>(`/commercial-packages/${id}/revisions`);
      setRevs((s) => ({ ...s, [id]: r.data ?? [] }));
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Failed"); }
  }

  async function handlePublish(payload: PublishPayload) {
    setBusy(true); setErr(null);
    try {
      await api.post("/commercial-packages", payload);
      setShowNew(false); await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Publish failed"); }
    finally { setBusy(false); }
  }

  async function toggleActive(p: PackageSummary) {
    setBusy(true); setErr(null);
    try {
      if (p.active) {
        const reason = window.prompt("Why are you withdrawing this package? Guests will stop seeing it immediately.");
        if (!reason) { setBusy(false); return; }
        const password = window.prompt("Confirm your password to deactivate");
        if (!password) { setBusy(false); return; }
        await api.post(`/commercial-packages/${p.package_id}/active`, { active: false, reason, password });
      } else {
        await api.post(`/commercial-packages/${p.package_id}/active`, { active: true });
      }
      await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Update failed"); }
    finally { setBusy(false); }
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button onClick={() => setShowNew((v) => !v)}>{showNew ? <X size={16} /> : <Plus size={16} />}{showNew ? "Cancel" : "Publish package"}</Button>
      </div>
      {showNew && (
        <Card>
          <CardHeader><CardTitle>New package</CardTitle></CardHeader>
          <CardBody>
            <PublishPackageForm plans={plans} busy={busy} onPublish={handlePublish} />
          </CardBody>
        </Card>
      )}
      <Card><CardBody>
        {rows && rows.length === 0 ? (
          <EmptyState title="No internet packages yet"
            hint="Until a package exists, a verified guest has nothing to be given and cannot get online. Create a service plan first, then a package that uses it." />
        ) : (
          <Table>
            <THead><TR><TH>Package</TH><TH>Status</TH><TH>Revisions</TH><TH></TH></TR></THead>
            <tbody>
              {(rows ?? []).map((p) => (
                <Fragment key={p.package_id}>
                  <TR key={p.package_id}>
                    <TD className="font-medium">{p.code}</TD>
                    <TD>{p.active ? <Badge tone="ok">Offered to guests</Badge> : <Badge tone="default">Not offered</Badge>}</TD>
                    <TD><button className="underline text-muted" onClick={() => toggleRevs(p.package_id)}>{p.revision_count} revision{p.revision_count === 1 ? "" : "s"} ▾</button></TD>
                    <TD><Button variant="ghost" disabled={busy} onClick={() => toggleActive(p)}>{p.active ? "Stop offering" : "Start offering"}</Button></TD>
                  </TR>
                  {revs[p.package_id] && (
                    <TR key={p.package_id + "-revs"}>
                      <TD className="text-xs text-muted" >History</TD>
                      <TD className="text-xs text-muted" ></TD>
                      <TD className="text-xs" >
                        {revs[p.package_id].map((r) => (
                          <div key={r.revision_id}>#{r.revision_no} {r.is_current ? <Badge tone="info">current · immutable</Badge> : <span className="text-muted">immutable</span>} {r.package_type} {r.price_minor === 0 ? "free" : `${r.price_minor} ${r.currency}`}</div>
                        ))}
                      </TD>
                      <TD></TD>
                    </TR>
                  )}
                </Fragment>
              ))}
            </tbody>
          </Table>
        )}
      </CardBody></Card>
    </div>
  );
}

// PlansTab and GraceTab lived here and are gone, not commented out. Service plans are now /service-plans
// (which also shows what each plan grants, which the tab could not) and checkout grace is /checkout-grace.
// Leaving them behind a hidden tab would have recreated the duplicate-surface problem this pass exists to
// remove — two places to publish a plan, differing in what they can express.

function InspectionTab({ guard, setErr }: TabProps) {
  const [quotes, setQuotes] = useState<QuoteInspect[]>([]);
  const [purchases, setPurchases] = useState<PurchaseInspect[]>([]);

  useEffect(() => {
    (async () => {
      try {
        const [q, p] = await Promise.all([
          api.get<ListResp<QuoteInspect>>("/commercial-packages/quotes"),
          api.get<ListResp<PurchaseInspect>>("/commercial-packages/purchases"),
        ]);
        setQuotes(q.data ?? []); setPurchases(p.data ?? []);
      } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Failed to load"); }
    })();
  }, [guard, setErr]);

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader><CardTitle>Packages offered to guests</CardTitle></CardHeader>
        <CardBody>
          {quotes.length === 0 ? <EmptyState title="No quotes" /> : (
            <Table>
              <THead><TR><TH>ID</TH><TH>Revision</TH><TH>Price</TH><TH>Expires</TH><TH>Consumed</TH></TR></THead>
              <tbody>{quotes.map((q) => (
                <TR key={q.id}><TD className="font-mono text-xs">{q.id}</TD><TD className="font-mono text-xs">{q.package_revision_id}</TD>
                  <TD>{q.price_minor === 0 ? "free" : `${q.price_minor} ${q.currency}`}</TD><TD className="text-xs">{q.expires_at}</TD><TD className="text-xs">{q.consumed_at || "—"}</TD></TR>
              ))}</tbody>
            </Table>
          )}
        </CardBody>
      </Card>
      <Card>
        <CardHeader><CardTitle>Packages taken by guests</CardTitle></CardHeader>
        <CardBody>
          {purchases.length === 0 ? <EmptyState title="No purchases" /> : (
            <Table>
              <THead><TR><TH>ID</TH><TH>Revision</TH><TH>State</TH><TH>Amount</TH></TR></THead>
              <tbody>{purchases.map((p) => (
                <TR key={p.id}><TD className="font-mono text-xs">{p.id}</TD><TD className="font-mono text-xs">{p.package_revision_id}</TD>
                  <TD><Badge tone={p.state === "GRANTED" ? "ok" : "default"}>{p.state}</Badge></TD><TD>{p.amount_minor === 0 ? "free" : `${p.amount_minor} ${p.currency}`}</TD></TR>
              ))}</tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
