"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  api, ListResp, Whoami, Interface, Pool,
  GuestNetworkInput, ValidateResult, ApplyResult, ValidationIssue,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ArrowLeft, Plus, X } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";

const STEPS = ["Identity", "Interface / VLAN", "Subnet & gateway", "DHCP & DNS", "Captive portal", "Review", "Apply"];

// interfaces whose role permits a guest network as parent
const SELECTABLE = new Set(["guest_access", "guest_trunk", "unused"]);

export default function NewGuestNetworkPage() {
  const router = useRouter();
  const [roles, setRoles] = useState<string[]>([]);
  const [step, setStep] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // identity
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [ssidLabel, setSsidLabel] = useState("");
  // interface / vlan
  const [interfaces, setInterfaces] = useState<Interface[] | null>(null);
  const [parentInterface, setParentInterface] = useState("");
  const [vlanTagged, setVlanTagged] = useState(false);
  const [vlanId, setVlanId] = useState("");
  // subnet / gateway
  const [subnetCidr, setSubnetCidr] = useState("");
  const [gatewayIp, setGatewayIp] = useState("");
  // dhcp / dns
  const [pools, setPools] = useState<Pool[]>([{ start_ip: "", end_ip: "" }]);
  const [dnsMode, setDnsMode] = useState<"appliance" | "custom">("appliance");
  const [dnsServers, setDnsServers] = useState("");
  const [domainName, setDomainName] = useState("guest.local");
  const [leaseDefault, setLeaseDefault] = useState("3600");
  const [leaseMin, setLeaseMin] = useState("900");
  const [leaseMax, setLeaseMax] = useState("7200");
  // captive portal
  const [captivePortal, setCaptivePortal] = useState(true);
  const [internetAccess, setInternetAccess] = useState(true);
  const [nat, setNat] = useState(true);
  const [clientIsolation, setClientIsolation] = useState(false);

  // apply flow
  const [created, setCreated] = useState<{ id: string; bridge_name: string; portal_url: string } | null>(null);
  const [issues, setIssues] = useState<ValidationIssue[] | null>(null);
  const [applied, setApplied] = useState<ApplyResult | null>(null);

  const writable = canWrite("network", roles);

  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);

  // fetch interfaces when entering step 1
  useEffect(() => {
    if (step === 1 && interfaces === null) {
      api.get<{ interfaces: Interface[] }>("/network/interfaces")
        .then((r) => setInterfaces(r.interfaces ?? []))
        .catch((e) => setErr(errMsg(e)));
    }
  }, [step, interfaces]);

  function stepError(i: number): string | null {
    switch (i) {
      case 0:
        if (!name.trim()) return "Name is required.";
        return null;
      case 1:
        if (!parentInterface) return "Pick a parent interface.";
        if (vlanTagged && (!vlanId || Number(vlanId) < 1 || Number(vlanId) > 4094)) return "VLAN id must be 1..4094.";
        return null;
      case 2:
        if (!subnetCidr.trim()) return "Subnet CIDR is required (e.g. 10.20.0.0/22).";
        if (!gatewayIp.trim()) return "Gateway IP is required.";
        return null;
      case 3:
        if (pools.length === 0 || pools.some((p) => !p.start_ip.trim() || !p.end_ip.trim()))
          return "Add at least one DHCP pool with a start and end address.";
        if (dnsMode === "custom" && !dnsServers.trim()) return "Provide at least one DNS server.";
        return null;
      default:
        return null;
    }
  }

  function next() {
    const e = stepError(step);
    if (e) { setErr(e); return; }
    setErr(null);
    setStep((s) => Math.min(STEPS.length - 1, s + 1));
  }
  function back() { setErr(null); setStep((s) => Math.max(0, s - 1)); }

  function buildBody(): GuestNetworkInput {
    return {
      name: name.trim(),
      description: description.trim() || undefined,
      ssid_label: ssidLabel.trim() || undefined,
      network_type: vlanTagged ? "vlan" : "untagged",
      parent_interface: parentInterface,
      vlan_id: vlanTagged ? Number(vlanId) : undefined,
      gateway_ip: gatewayIp.trim(),
      subnet_cidr: subnetCidr.trim(),
      dhcp_mode: "local",
      dns_mode: dnsMode,
      dns_servers: dnsMode === "custom"
        ? dnsServers.split(",").map((s) => s.trim()).filter(Boolean)
        : undefined,
      domain_name: domainName.trim() || "guest.local",
      lease_default_seconds: Number(leaseDefault) || 3600,
      lease_min_seconds: Number(leaseMin) || 900,
      lease_max_seconds: Number(leaseMax) || 7200,
      captive_portal_enabled: captivePortal,
      internet_access_enabled: internetAccess,
      nat_enabled: nat,
      client_isolation_enabled: clientIsolation,
      pools: pools.filter((p) => p.start_ip && p.end_ip),
    };
  }

  async function onRunApply() {
    setBusy(true); setErr(null); setIssues(null);
    try {
      let c = created;
      if (!c) {
        c = await api.post<{ id: string; bridge_name: string; portal_url: string }>(
          "/network/guest-networks", buildBody());
        setCreated(c);
      }
      const v = await api.post<ValidateResult>("/network/validate");
      if (!v.validation.ok) {
        setIssues(v.validation.issues ?? []);
        setBusy(false);
        return;
      }
      const a = await api.post<ApplyResult>("/network/apply", { summary: `create guest network ${name}` });
      setApplied(a);
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onConfirm() {
    if (!applied) return;
    setBusy(true); setErr(null);
    try { await api.post(`/network/revisions/${applied.revision_id}/confirm`); router.push("/network"); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onRollback() {
    if (!applied) return;
    setBusy(true); setErr(null);
    try { await api.post(`/network/revisions/${applied.revision_id}/rollback`); setApplied(null); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  const portalNote = gatewayIp ? `http://${gatewayIp}:8380` : "the gateway IP on port 8380";

  if (!writable) {
    return (
      <div className="p-6 max-w-3xl mx-auto">
        <Link href="/network" className="text-sm text-muted hover:text-text inline-flex items-center gap-1 mb-4">
          <ArrowLeft size={14} /> Back to guest networks
        </Link>
        <Card><CardBody>You do not have permission to create guest networks.</CardBody></Card>
      </div>
    );
  }

  return (
    <div className="p-6 max-w-3xl mx-auto">
      <Link href="/network" className="text-sm text-muted hover:text-text inline-flex items-center gap-1 mb-4">
        <ArrowLeft size={14} /> Back to guest networks
      </Link>

      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">Networking</div>
        <h1 className="text-2xl font-semibold">New guest network</h1>
      </div>

      {/* stepper */}
      <div className="flex flex-wrap gap-2 mb-6 text-xs">
        {STEPS.map((label, i) => (
          <div
            key={label}
            className={
              "px-2 py-1 rounded border " +
              (i === step
                ? "bg-brand text-white border-brand"
                : i < step
                ? "bg-panel2 text-text border-border"
                : "bg-panel2 text-muted border-border")
            }
          >
            {i + 1}. {label}
          </div>
        ))}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <Card>
        <CardHeader><CardTitle>{STEPS[step]}</CardTitle></CardHeader>
        <CardBody className="space-y-4">
          {step === 0 && (
            <>
              <div><Label>Name</Label><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Guest WiFi" /></div>
              <div><Label>Description</Label><Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" /></div>
              <div>
                <Label>SSID label</Label>
                <Input value={ssidLabel} onChange={(e) => setSsidLabel(e.target.value)} placeholder="Broadcast SSID name (for reference)" />
                <div className="text-xs text-muted mt-1">StayConnect does not broadcast WiFi — this label maps the SSID your controller broadcasts to this gateway.</div>
              </div>
            </>
          )}

          {step === 1 && (
            <>
              <div>
                <Label>Parent interface</Label>
                {interfaces === null ? (
                  <div className="text-sm text-muted">Loading interfaces…</div>
                ) : (
                  <div className="space-y-2">
                    {interfaces.map((n) => {
                      const selectable = SELECTABLE.has(n.role ?? "");
                      return (
                        <label
                          key={n.name}
                          className={
                            "flex items-center justify-between gap-3 rounded-md border px-3 py-2 " +
                            (selectable ? "border-border cursor-pointer hover:bg-panel2" : "border-border opacity-50 cursor-not-allowed")
                          }
                        >
                          <div className="flex items-center gap-2">
                            <input
                              type="radio" name="parent" disabled={!selectable}
                              checked={parentInterface === n.name}
                              onChange={() => setParentInterface(n.name)}
                            />
                            <span className="font-mono text-sm">{n.name}</span>
                            <span className="text-xs text-muted">{n.mac} · {n.link_state} · mtu {n.mtu}</span>
                          </div>
                          <Badge tone={selectable ? "info" : "default"}>{n.role ?? "unknown"}</Badge>
                        </label>
                      );
                    })}
                    {interfaces.length === 0 && <div className="text-sm text-muted">No interfaces discovered.</div>}
                  </div>
                )}
              </div>
              <label className="flex items-center gap-2 text-sm text-muted">
                <input type="checkbox" checked={vlanTagged} onChange={(e) => setVlanTagged(e.target.checked)} /> VLAN tagged (802.1Q)
              </label>
              {vlanTagged && (
                <div className="max-w-[12rem]">
                  <Label>VLAN id</Label>
                  <Input type="number" min={1} max={4094} value={vlanId} onChange={(e) => setVlanId(e.target.value)} placeholder="e.g. 20" />
                </div>
              )}
            </>
          )}

          {step === 2 && (
            <>
              <div>
                <Label>Subnet CIDR</Label>
                <Input value={subnetCidr} onChange={(e) => setSubnetCidr(e.target.value)} placeholder="10.20.0.0/22" />
              </div>
              <div>
                <Label>Gateway IP</Label>
                <Input value={gatewayIp} onChange={(e) => setGatewayIp(e.target.value)} placeholder="10.20.0.1" />
                <div className="text-xs text-muted mt-1">
                  The appliance owns this address on the bridge; guests use it as their default gateway and DNS.
                </div>
              </div>
            </>
          )}

          {step === 3 && (
            <>
              <div>
                <Label>DHCP pools</Label>
                <div className="space-y-2">
                  {pools.map((p, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <Input placeholder="start (10.20.0.100)" value={p.start_ip}
                        onChange={(e) => setPools((ps) => ps.map((x, k) => k === i ? { ...x, start_ip: e.target.value } : x))} />
                      <span className="text-muted">–</span>
                      <Input placeholder="end (10.20.3.250)" value={p.end_ip}
                        onChange={(e) => setPools((ps) => ps.map((x, k) => k === i ? { ...x, end_ip: e.target.value } : x))} />
                      <Button size="sm" variant="ghost" disabled={pools.length === 1}
                        onClick={() => setPools((ps) => ps.filter((_, k) => k !== i))}><X size={14} /></Button>
                    </div>
                  ))}
                </div>
                <Button size="sm" variant="secondary" className="mt-2"
                  onClick={() => setPools((ps) => [...ps, { start_ip: "", end_ip: "" }])}>
                  <Plus size={14} /> Add pool
                </Button>
              </div>
              <div>
                <Label>DNS mode</Label>
                <select value={dnsMode} onChange={(e) => setDnsMode(e.target.value as "appliance" | "custom")}
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  <option value="appliance">appliance (resolve on the gateway)</option>
                  <option value="custom">custom servers</option>
                </select>
              </div>
              {dnsMode === "custom" && (
                <div><Label>DNS servers (comma)</Label><Input value={dnsServers} onChange={(e) => setDnsServers(e.target.value)} placeholder="1.1.1.1, 9.9.9.9" /></div>
              )}
              <div><Label>Domain name</Label><Input value={domainName} onChange={(e) => setDomainName(e.target.value)} placeholder="guest.local" /></div>
              <div className="grid grid-cols-3 gap-3">
                <div><Label>Lease default (s)</Label><Input type="number" value={leaseDefault} onChange={(e) => setLeaseDefault(e.target.value)} /></div>
                <div><Label>Lease min (s)</Label><Input type="number" value={leaseMin} onChange={(e) => setLeaseMin(e.target.value)} /></div>
                <div><Label>Lease max (s)</Label><Input type="number" value={leaseMax} onChange={(e) => setLeaseMax(e.target.value)} /></div>
              </div>
            </>
          )}

          {step === 4 && (
            <>
              <label className="flex items-center gap-2 text-sm text-muted">
                <input type="checkbox" checked={captivePortal} onChange={(e) => setCaptivePortal(e.target.checked)} /> Captive portal enabled
              </label>
              <label className="flex items-center gap-2 text-sm text-muted">
                <input type="checkbox" checked={internetAccess} onChange={(e) => setInternetAccess(e.target.checked)} /> Internet access enabled
              </label>
              <label className="flex items-center gap-2 text-sm text-muted">
                <input type="checkbox" checked={nat} onChange={(e) => setNat(e.target.checked)} /> NAT (masquerade) enabled
              </label>
              <label className="flex items-center gap-2 text-sm text-muted">
                <input type="checkbox" checked={clientIsolation} onChange={(e) => setClientIsolation(e.target.checked)} /> Client isolation
              </label>
              <div className="text-xs text-muted">
                The captive portal will be served automatically at <span className="font-mono">{portalNote}</span> once applied.
              </div>
            </>
          )}

          {step === 5 && (
            <div className="space-y-3 text-sm">
              <Summary label="Name" value={name} />
              <Summary label="SSID label" value={ssidLabel || "—"} />
              <Summary label="Type" value={vlanTagged ? `VLAN ${vlanId}` : "untagged"} />
              <Summary label="Parent interface" value={parentInterface} />
              <Summary label="Subnet" value={subnetCidr} />
              <Summary label="Gateway" value={gatewayIp} />
              <Summary label="Pools" value={pools.filter((p) => p.start_ip).map((p) => `${p.start_ip}–${p.end_ip}`).join(", ") || "—"} />
              <Summary label="DNS" value={dnsMode === "custom" ? dnsServers : "appliance"} />
              <Summary label="Captive portal" value={captivePortal ? "on" : "off"} />
              <Summary label="Internet / NAT / isolation" value={`${internetAccess ? "internet" : "no-internet"} · ${nat ? "nat" : "no-nat"} · ${clientIsolation ? "isolated" : "open"}`} />
              <div className="mt-4 rounded-md border border-[#6b4e1c] bg-[#3a2a0e] text-warn text-sm px-4 py-3">
                <div className="font-medium">Wireless controller action required</div>
                <div className="text-xs mt-1">
                  Map the &lsquo;{ssidLabel || name}&rsquo; SSID to VLAN {vlanTagged ? vlanId : "(untagged)"} on your wireless controller.
                  StayConnect manages the gateway, DHCP and captive portal.
                </div>
              </div>
            </div>
          )}

          {step === 6 && (
            <div className="space-y-4 text-sm">
              {!created && !applied && (
                <div className="text-muted">
                  Ready to create the guest network, validate the full configuration, then apply it.
                </div>
              )}
              {created && (
                <div className="rounded-md border border-border bg-panel2 px-4 py-3 space-y-1">
                  <div className="text-ok">Guest network created.</div>
                  <div className="text-xs">Bridge <span className="font-mono">{created.bridge_name}</span></div>
                  <div className="text-xs">Portal <span className="font-mono">{created.portal_url}</span></div>
                </div>
              )}
              {issues && issues.length > 0 && (
                <div className="rounded-md border border-[#6b2128] bg-[#3a1418] px-4 py-3">
                  <div className="text-err font-medium mb-1">Validation failed</div>
                  <ul className="space-y-1">
                    {issues.map((i, k) => (
                      <li key={k} className="text-err text-xs">
                        <span className="font-mono">{i.field}</span> — {i.message} <span className="text-muted">({i.code})</span>
                      </li>
                    ))}
                  </ul>
                  <div className="text-xs text-muted mt-2">Go back to fix these, then save again (the network is already created — editing it from the list applies the fixes).</div>
                </div>
              )}
              {applied && (
                <div className={
                  "rounded-md border px-4 py-3 " +
                  (applied.state === "pending_confirmation"
                    ? "border-[#6b4e1c] bg-[#3a2a0e] text-warn"
                    : applied.state === "rolled_back" || applied.state === "failed"
                    ? "border-[#6b2128] bg-[#3a1418] text-err"
                    : "border-[#1e5c3c] bg-[#123422] text-ok")
                }>
                  <div className="font-medium">
                    {applied.state === "pending_confirmation" ? "Applied — pending confirmation" : `Apply state: ${applied.state}`}
                  </div>
                  {applied.state === "pending_confirmation" && (
                    <div className="text-xs mt-1">Confirm within 120s or the configuration rolls back automatically.</div>
                  )}
                  {applied.message && <div className="text-xs mt-1">{applied.message}</div>}
                  {applied.health && applied.health.length > 0 && (
                    <ul className="mt-2 space-y-1">
                      {applied.health.map((h, k) => (
                        <li key={k} className="flex items-center gap-2 text-xs">
                          <Badge tone={h.ok ? "ok" : "err"}>{h.ok ? "ok" : "fail"}</Badge>
                          <span className="font-mono">{h.name}</span>
                          {h.detail && <span className="text-muted">{h.detail}</span>}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
              <div className="flex gap-2">
                {!applied && (
                  <Button disabled={busy} onClick={onRunApply}>
                    {busy ? "Working…" : created ? "Re-validate & apply" : "Create, validate & apply"}
                  </Button>
                )}
                {applied?.state === "pending_confirmation" && (
                  <>
                    <Button disabled={busy} onClick={onConfirm}>Confirm</Button>
                    <Button variant="secondary" disabled={busy} onClick={onRollback}>Rollback</Button>
                  </>
                )}
                {applied && applied.state !== "pending_confirmation" && (
                  <Link href="/network" className="inline-flex items-center gap-2 h-9 px-4 text-sm rounded-md bg-panel2 border border-border hover:bg-[#222735]">
                    Back to guest networks
                  </Link>
                )}
              </div>
            </div>
          )}
        </CardBody>
      </Card>

      {step < 6 && (
        <div className="flex justify-between mt-4">
          <Button variant="secondary" onClick={back} disabled={step === 0}>Back</Button>
          <Button onClick={next}>Next</Button>
        </div>
      )}
      {step === 6 && !applied && (
        <div className="flex justify-between mt-4">
          <Button variant="secondary" onClick={back} disabled={busy}>Back</Button>
        </div>
      )}
    </div>
  );
}

function Summary({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between border-b border-border py-1">
      <span className="text-muted">{label}</span>
      <span className="font-mono text-xs text-right">{value}</span>
    </div>
  );
}
