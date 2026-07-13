"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { api, reauth, withStepUp, ApiError, BootstrapTokenCreated, Plan, ListResp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Check, Copy, Loader2, Circle, RotateCcw, Plug, ShieldCheck } from "lucide-react";

/**
 * Connect an Appliance — one-page onboarding.
 *
 * The operator does everything from here: pick (or create) the customer, site and
 * plan, then generate ONE enrollment token to paste on the appliance. The moment
 * the appliance enrolls, this page automatically runs the rest of the lifecycle —
 * claim → assign → issue certificate → issue license — and shows live progress.
 * The vendor-signed assignment the appliance adopts is minted by the assign step;
 * nothing is ever hand-edited on the box.
 */

type Tenant = { id: string; slug: string; name: string };
type Site = { id: string; code: string; name: string };
type Pending = { id: string; serial: string; state: string };
type AssignmentStatus = { issued: boolean; version?: number; state?: string };
type ApplianceRow = { id: string; serial: string; status?: string; lifecycle_state?: string; last_seen_at?: string };

// Lifecycle steps shown in the live tracker (after the token is generated).
type StepKey = "waiting" | "enrolled" | "claimed" | "assigned" | "cert" | "licensed" | "online";
const STEPS: { key: StepKey; label: string; hint?: string }[] = [
  { key: "waiting", label: "Waiting for appliance to enroll", hint: "Paste the token on the appliance" },
  { key: "enrolled", label: "Appliance enrolled" },
  { key: "claimed", label: "Claimed" },
  { key: "assigned", label: "Assigned to customer + site" },
  { key: "cert", label: "Certificate issued (mTLS)" },
  { key: "licensed", label: "License issued" },
  { key: "online", label: "Appliance online" },
];
const STEP_ORDER: StepKey[] = STEPS.map((s) => s.key);
const stepIndex = (k: StepKey) => STEP_ORDER.indexOf(k);

function slugify(s: string): string {
  return s.toLowerCase().trim().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40);
}

export default function OnboardingPage() {
  const router = useRouter();
  // ---- form state ----
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [sites, setSites] = useState<Site[]>([]);

  const [tenantMode, setTenantMode] = useState<"existing" | "new">("existing");
  const [tenantId, setTenantId] = useState("");
  const [newCustomer, setNewCustomer] = useState("");

  const [siteMode, setSiteMode] = useState<"existing" | "new">("existing");
  const [siteId, setSiteId] = useState("");
  const [newSite, setNewSite] = useState("");

  const [planId, setPlanId] = useState("");        // "" = keep current
  const [serial, setSerial] = useState("");
  const [password, setPassword] = useState("");    // primes step-up so auto-run is seamless

  const [formErr, setFormErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // ---- run state (after token generated) ----
  const [token, setToken] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [phase, setPhase] = useState<StepKey>("waiting");
  const [trackedId, setTrackedId] = useState<string | null>(null);
  const [runErr, setRunErr] = useState<string | null>(null);
  const runCtx = useRef<{ tenant: string; site: string } | null>(null);
  const acting = useRef(false);

  // Load customers + plans once; sites when the customer changes.
  const loadBase = useCallback(async () => {
    try {
      const [t, p] = await Promise.all([
        api.get<{ data: Tenant[] }>("/v1/tenants"),
        api.get<ListResp<Plan>>("/v1/plans"),
      ]);
      setTenants(t.data ?? []);
      setPlans((p.data ?? []).filter((x) => x.is_active));
    } catch (e) {
      setFormErr(e instanceof Error ? e.message : "Failed to load");
    }
  }, []);
  useEffect(() => { loadBase(); }, [loadBase]);

  useEffect(() => {
    if (tenantMode !== "existing" || !tenantId) { setSites([]); return; }
    api.get<{ data: Site[] }>(`/v1/sites?tenant_id=${tenantId}`)
      .then((r) => setSites(r.data ?? []))
      .catch(() => setSites([]));
  }, [tenantMode, tenantId]);

  // Resolve the chosen (or newly-created) customer + site to concrete ids,
  // creating rows and setting the plan as needed. Returns { tenant, site }.
  async function resolveTargets(): Promise<{ tenant: string; site: string }> {
    // customer
    let tid = tenantId;
    if (tenantMode === "new") {
      const name = newCustomer.trim();
      if (!name) throw new Error("Enter a customer name.");
      const slug = slugify(name);
      await api.post("/v1/tenants", { slug, name });
      const r = await api.get<{ data: Tenant[] }>("/v1/tenants");
      const found = (r.data ?? []).find((x) => x.slug === slug);
      if (!found) throw new Error("Customer created but could not be resolved.");
      tid = found.id;
    }
    if (!tid) throw new Error("Pick or create a customer.");

    // plan (optional for an existing customer that already has one)
    if (planId) {
      await api.post(`/v1/tenants/${tid}/subscription`, { plan_id: planId });
    }

    // site
    let sid = siteMode === "existing" ? siteId : "";
    if (siteMode === "new") {
      const name = newSite.trim();
      if (!name) throw new Error("Enter a site name.");
      const code = slugify(name);
      const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
      await api.post(`/v1/sites?tenant_id=${tid}`, { code, name, timezone: tz });
      const r = await api.get<{ data: Site[] }>(`/v1/sites?tenant_id=${tid}`);
      const found = (r.data ?? []).find((x) => x.code === code);
      if (!found) throw new Error("Site created but could not be resolved.");
      sid = found.id;
    }
    if (!sid) throw new Error("Pick or create a site.");

    return { tenant: tid, site: sid };
  }

  async function onGenerate() {
    setFormErr(null);
    if (!serial.trim()) { setFormErr("Enter the appliance serial (shown on the appliance's setup screen)."); return; }
    if (tenantMode === "new" && plans.length > 0 && !planId) { setFormErr("Choose a plan for the new customer."); return; }
    if (!password.trim()) { setFormErr("Enter your password to authorize assignment, certificate and license."); return; }
    setBusy(true);
    try {
      // Prime step-up once so the automatic assign/cert/license don't each prompt.
      await reauth(password.trim());
      setPassword("");

      const targets = await resolveTargets();
      runCtx.current = targets;

      const minted = await api.post<BootstrapTokenCreated>(
        `/v1/appliance-bootstrap-tokens?tenant_id=${targets.tenant}`,
        { site_id: targets.site, expected_serial: serial.trim(), ttl_hours: 24 },
      );
      setToken(minted.token);
      setPhase("waiting");
      setTrackedId(null);
      setRunErr(null);
    } catch (e) {
      if (e instanceof ApiError && e.code === "reauth_required") setFormErr("Password confirmation failed — check your password.");
      else if (e instanceof ApiError && e.status === 401) setFormErr("Password confirmation failed — check your password.");
      else setFormErr(e instanceof Error ? e.message : "Could not start onboarding.");
    } finally {
      setBusy(false);
    }
  }

  // ---- auto-run tracker ----
  const advance = useCallback(async () => {
    if (!token || acting.current || phase === "online") return;
    const ctx = runCtx.current;
    if (!ctx) return;
    acting.current = true;
    try {
      // 1) discover the appliance by serial once it has enrolled
      if (!trackedId) {
        const p = await api.get<{ data: Pending[] }>("/cloud/v1/appliances-admin/pending");
        const match = (p.data ?? []).find((x) => x.serial === serial.trim());
        if (!match) return; // still waiting
        setTrackedId(match.id);
        // if it was partially onboarded already, jump ahead
        let start: StepKey = "enrolled";
        try {
          const a = await api.get<AssignmentStatus>(`/cloud/v1/appliances-admin/${match.id}/assignment`);
          if (a.issued) start = "cert";
        } catch { /* ignore */ }
        setPhase(start);
        return;
      }

      const id = trackedId;
      switch (phase) {
        case "enrolled":
          await withStepUp(() => api.post(`/cloud/v1/appliances-admin/${id}/claim`, {}));
          setPhase("claimed");
          break;
        case "claimed":
          await withStepUp(() =>
            api.post(`/cloud/v1/appliances-admin/${id}/assign`, {
              tenant_id: ctx.tenant, site_id: ctx.site, reason: "guided onboarding",
            }));
          setPhase("assigned");
          break;
        case "assigned":
          await withStepUp(() => api.post(`/cloud/v1/certificates/${id}/issue`, {}));
          setPhase("cert");
          break;
        case "cert":
          await withStepUp(() =>
            api.post(`/cloud/v1/licenses`, {
              tenant_id: ctx.tenant, site_id: ctx.site, appliance_id: id,
              valid_days: 365, offline_grace_days: 30,
            }));
          setPhase("licensed");
          break;
        case "licensed": {
          // done as far as Central is concerned; flip to online when the box reports in
          try {
            const r = await api.get<{ data: ApplianceRow[] }>(`/v1/appliances?tenant_id=${ctx.tenant}`);
            const me = (r.data ?? []).find((x) => x.serial === serial.trim());
            const status = (me?.status ?? me?.lifecycle_state ?? "").toLowerCase();
            if (status === "online" || status === "active" || status === "assigned") setPhase("online");
          } catch { /* keep waiting */ }
          break;
        }
      }
    } catch (e) {
      // Base session expired mid auto-run → recover to login rather than looping on 401.
      if (e instanceof ApiError && e.status === 401) { router.replace("/login"); return; }
      setRunErr(e instanceof Error ? e.message : "A step failed.");
    } finally {
      acting.current = false;
    }
  }, [token, phase, trackedId, serial, router]);

  useEffect(() => {
    if (!token || phase === "online") return;
    advance();
    const t = setInterval(advance, 3500);
    return () => clearInterval(t);
  }, [token, phase, advance]);

  function startOver() {
    setToken(null); setTrackedId(null); setPhase("waiting"); setRunErr(null);
    runCtx.current = null; acting.current = false;
    setSerial(""); setNewCustomer(""); setNewSite(""); setPlanId("");
    loadBase();
  }

  const selectCls = "w-full rounded-md border border-border bg-panel2 px-3 py-2 text-sm";
  const curPlanName = plans.find((p) => p.id === planId)?.name;

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <div className="mb-1 text-xs text-muted uppercase tracking-wider">Infrastructure</div>
      <h1 className="mb-1 flex items-center gap-2 text-2xl font-semibold"><Plug size={22} /> Connect an Appliance</h1>
      <p className="mb-6 text-sm text-muted">
        Pick the customer, site and plan, generate one token to paste on the appliance, and the rest
        runs automatically. No jumping between pages.
      </p>

      {!token ? (
        /* ---------------- SETUP FORM ---------------- */
        <Card>
          <CardHeader><CardTitle>Set up the hotel</CardTitle></CardHeader>
          <CardBody className="space-y-5">
            {/* Customer */}
            <div className="space-y-2">
              <Label>1 · Customer</Label>
              <div className="flex gap-2 text-sm">
                <button type="button" onClick={() => setTenantMode("existing")}
                  className={`rounded px-3 py-1 ${tenantMode === "existing" ? "bg-brand/25 text-brand" : "bg-panel2 text-muted"}`}>Existing</button>
                <button type="button" onClick={() => setTenantMode("new")}
                  className={`rounded px-3 py-1 ${tenantMode === "new" ? "bg-brand/25 text-brand" : "bg-panel2 text-muted"}`}>+ New customer</button>
              </div>
              {tenantMode === "existing" ? (
                <select className={selectCls} value={tenantId} onChange={(e) => { setTenantId(e.target.value); setSiteId(""); }}>
                  <option value="">Select a customer…</option>
                  {tenants.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
                </select>
              ) : (
                <Input placeholder="Customer name (e.g. Grand Hotel Group)" value={newCustomer} onChange={(e) => setNewCustomer(e.target.value)} />
              )}
            </div>

            {/* Site */}
            <div className="space-y-2">
              <Label>2 · Site</Label>
              <div className="flex gap-2 text-sm">
                <button type="button" onClick={() => setSiteMode("existing")} disabled={tenantMode === "new"}
                  className={`rounded px-3 py-1 disabled:opacity-40 ${siteMode === "existing" && tenantMode !== "new" ? "bg-brand/25 text-brand" : "bg-panel2 text-muted"}`}>Existing</button>
                <button type="button" onClick={() => setSiteMode("new")}
                  className={`rounded px-3 py-1 ${siteMode === "new" || tenantMode === "new" ? "bg-brand/25 text-brand" : "bg-panel2 text-muted"}`}>+ New site</button>
              </div>
              {siteMode === "existing" && tenantMode === "existing" ? (
                <select className={selectCls} value={siteId} onChange={(e) => setSiteId(e.target.value)} disabled={!tenantId}>
                  <option value="">Select a site…</option>
                  {sites.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                </select>
              ) : (
                <Input placeholder="Site name (e.g. Main Building)" value={newSite} onChange={(e) => setNewSite(e.target.value)} />
              )}
            </div>

            {/* Plan */}
            <div className="space-y-2">
              <Label>3 · Plan</Label>
              <select className={selectCls} value={planId} onChange={(e) => setPlanId(e.target.value)}>
                <option value="">{tenantMode === "existing" ? "Keep current plan" : "Select a plan…"}</option>
                {plans.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name} — {(p.price_cents / 100).toFixed(0)} {p.currency}/{p.billing_cycle === "yearly" ? "yr" : "mo"}
                  </option>
                ))}
              </select>
            </div>

            {/* Serial + password */}
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1">
                <Label htmlFor="ob-serial">Appliance serial</Label>
                <Input id="ob-serial" placeholder="e.g. APP-DEV-0001" value={serial} onChange={(e) => setSerial(e.target.value)} />
                <p className="text-xs text-muted">Shown on the appliance&apos;s setup screen. Locks the token to this box.</p>
              </div>
              <div className="space-y-1">
                <Label htmlFor="ob-pw">Confirm your password</Label>
                <Input id="ob-pw" type="password" autoComplete="off" placeholder="authorizes assign / cert / license"
                  value={password} onChange={(e) => setPassword(e.target.value)} />
                <p className="text-xs text-muted">Entered once so the rest runs without interruption.</p>
              </div>
            </div>

            {formErr && <div className="text-sm text-err">{formErr}</div>}

            <Button onClick={onGenerate} disabled={busy}>
              {busy ? <><Loader2 className="mr-1 h-4 w-4 animate-spin" /> Preparing…</> : "Generate enrollment token"}
            </Button>
          </CardBody>
        </Card>
      ) : (
        /* ---------------- TOKEN + LIVE TRACKER ---------------- */
        <div className="grid gap-6 lg:grid-cols-2">
          <Card>
            <CardHeader><CardTitle>Paste this on the appliance</CardTitle></CardHeader>
            <CardBody className="space-y-3">
              <p className="text-sm text-muted">
                Open the appliance&apos;s Hotel Admin → <b>Setup</b>, paste this token and enter serial{" "}
                <code className="font-mono">{serial}</code>, then press Connect. This screen finishes the rest.
              </p>
              <div className="flex items-stretch gap-2">
                <code className="flex-1 break-all rounded-md border border-border bg-panel2 px-3 py-2 font-mono text-sm">{token}</code>
                <Button variant="secondary" onClick={() => { navigator.clipboard?.writeText(token); setCopied(true); setTimeout(() => setCopied(false), 1500); }}>
                  {copied ? <><Check className="mr-1 h-4 w-4" /> Copied</> : <><Copy className="mr-1 h-4 w-4" /> Copy</>}
                </Button>
              </div>
              <p className="text-xs text-muted">One-time use · serial-locked · expires in 24h. Keep this tab open — onboarding continues automatically.</p>
              <Button variant="ghost" onClick={startOver}><RotateCcw className="mr-1 h-4 w-4" /> Start over</Button>
            </CardBody>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                {phase === "online" ? <ShieldCheck className="h-4 w-4 text-ok" /> : <Loader2 className="h-4 w-4 animate-spin text-brand" />}
                {phase === "online" ? "Appliance connected" : "Connecting…"}
              </CardTitle>
            </CardHeader>
            <CardBody>
              <ol className="space-y-3">
                {STEPS.map((s) => {
                  const done = stepIndex(phase) > stepIndex(s.key) || phase === "online";
                  const active = s.key === phase && phase !== "online";
                  return (
                    <li key={s.key} className="flex items-start gap-3 text-sm">
                      {done ? <Check className="mt-0.5 h-5 w-5 shrink-0 text-ok" />
                        : active ? <Loader2 className="mt-0.5 h-5 w-5 shrink-0 animate-spin text-brand" />
                        : <Circle className="mt-0.5 h-5 w-5 shrink-0 text-muted/40" />}
                      <div>
                        <div className={done ? "text-muted" : active ? "font-medium text-text" : "text-muted"}>{s.label}</div>
                        {active && s.hint && <div className="text-xs text-muted">{s.hint}</div>}
                      </div>
                    </li>
                  );
                })}
              </ol>

              {runErr && (
                <div className="mt-4 rounded border border-[#6b2128] bg-[#3a1418] p-2 text-sm text-err">
                  {runErr} <button className="underline" onClick={() => { setRunErr(null); advance(); }}>Retry</button>
                </div>
              )}
              {phase === "online" && (
                <div className="mt-4 flex items-center gap-2">
                  <Badge tone="ok">Onboarding complete</Badge>
                  <Button size="sm" variant="secondary" onClick={startOver}>Connect another</Button>
                </div>
              )}
            </CardBody>
          </Card>
        </div>
      )}
    </div>
  );
}
