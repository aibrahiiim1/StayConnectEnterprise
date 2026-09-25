"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  api, ListResp, Interface, Pool, NetRevision,
  GuestNetworkInput, ValidateResult, ApplyResult, ValidationIssue,
} from "@/lib/api";
import { Card, CardBody, CardFooter, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button, buttonVariants } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { PageShell, PageHeader } from "@/components/ui/page";
import { KeyValueGrid, OptionCard, Stepper } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { NotAvailable, PendingChangeBanner } from "@/components/ui/patterns";
import { EmptyState } from "@/components/ui/empty-state";
import { ArrowLeft, ArrowRight, Cable, Network, Plus, Radio, X } from "lucide-react";
import { cn, errMsg } from "@/lib/utils";
import { HealthCheckList, SwitchRow, ValidationIssueList, useNetworkAccess } from "@/components/network/shared";

const STEPS = ["Identity", "Interface / VLAN", "Subnet & gateway", "DHCP & DNS", "Captive portal", "Review", "Apply"];

const STEP_HINT = [
  "What the network is called here, and the SSID your wireless controller broadcasts for it.",
  "The port the guest traffic arrives on, and whether it is tagged with a VLAN.",
  "The address range guests get, and the gateway address the appliance takes on it.",
  "Which addresses are handed out, which DNS guests use, and for how long an address is kept.",
  "What guests see and can reach once they join.",
  "Check everything before it is created.",
  "Create the network, validate the whole configuration, then apply it with an automatic rollback.",
];

// interfaces whose role permits a guest network as parent
const SELECTABLE = new Set(["guest_access", "guest_trunk", "unused"]);

const ROLE_LABEL: Record<string, string> = {
  guest_access: "Guest access",
  guest_trunk: "Guest trunk",
  unused: "Unused",
  management: "Management",
  wan: "Uplink (WAN)",
  ha_sync: "HA sync",
};

export default function NewGuestNetworkPage() {
  const router = useRouter();
  const { known, writable } = useNetworkAccess();
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
  const [deadline, setDeadline] = useState<string | null>(null);
  const [acting, setActing] = useState<"confirm" | "rollback" | null>(null);

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

  // The apply response carries no deadline; the revision list does. Read it so the countdown is the appliance's
  // real one rather than a guess. Best-effort: without it the banner still offers Keep / Roll back.
  async function readDeadline(revisionId: string) {
    try {
      const r = await api.get<ListResp<NetRevision>>("/network/revisions");
      const rev = (r.data ?? []).find((x) => x.id === revisionId);
      setDeadline(rev?.confirm_deadline ?? null);
    } catch { /* the banner works without it */ }
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
      if (a.state === "pending_confirmation") void readDeadline(a.revision_id);
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onConfirm() {
    if (!applied) return;
    setActing("confirm"); setErr(null);
    try { await api.post(`/network/revisions/${applied.revision_id}/confirm`); router.push("/network"); }
    catch (e) { setErr(errMsg(e)); }
    finally { setActing(null); }
  }

  async function onRollback() {
    if (!applied) return;
    setActing("rollback"); setErr(null);
    try { await api.post(`/network/revisions/${applied.revision_id}/rollback`); setApplied(null); setDeadline(null); }
    catch (e) { setErr(errMsg(e)); }
    finally { setActing(null); }
  }

  const portalNote = gatewayIp ? `http://${gatewayIp}:8380` : "the gateway IP on port 8380";
  const poolText = pools.filter((p) => p.start_ip).map((p) => `${p.start_ip}–${p.end_ip}`).join(", ") || "—";
  const onOff = (b: boolean) => (b ? <Badge tone="ok">On</Badge> : <Badge tone="default">Off</Badge>);

  const backLink = (
    <Link href="/network" className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "-ms-3 self-start")}>
      <ArrowLeft /> Guest networks
    </Link>
  );

  if (!known) {
    return (
      <PageShell width="narrow">
        {backLink}
        <PageHeader icon={<Network />} eyebrow="Networking" title="New guest network" />
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-72 w-full" />
      </PageShell>
    );
  }

  if (!writable) {
    return (
      <PageShell width="narrow">
        {backLink}
        <PageHeader icon={<Network />} eyebrow="Networking" title="New guest network" />
        <NotAvailable
          title="You cannot create guest networks"
          reason="Your role can view guest networks but not change them. Ask a Hotel IT manager or site admin."
        />
      </PageShell>
    );
  }

  return (
    <PageShell width="narrow">
      {backLink}
      <PageHeader
        icon={<Network />}
        eyebrow="Networking"
        title="New guest network"
        description="Seven short steps. Nothing reaches guests until the last one, and even then the change rolls back on its own unless you keep it."
      />

      <nav aria-label="Steps">
        <Stepper steps={STEPS} current={step} onStep={applied ? undefined : (i) => { setErr(null); setStep(i); }} />
      </nav>

      <ErrorBanner err={err} className="mb-0" />

      <Card>
        <CardHeader>
          <div className="space-y-1">
            <CardTitle>
              <span className="text-muted-foreground">Step {step + 1} of {STEPS.length} · </span>{STEPS[step]}
            </CardTitle>
            <CardDescription>{STEP_HINT[step]}</CardDescription>
          </div>
        </CardHeader>
        <CardBody className="space-y-4">
          {step === 0 && (
            <>
              <Field label="Name" required>
                <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Guest Wi-Fi" autoFocus />
              </Field>
              <Field label="Description" hint="Optional. Shown under the name in the list.">
                <Input value={description} onChange={(e) => setDescription(e.target.value)} />
              </Field>
              <Field
                label="SSID label"
                hint="For reference only. OneGate does not broadcast Wi-Fi — this label records which SSID your wireless controller maps to this network."
              >
                <Input value={ssidLabel} onChange={(e) => setSsidLabel(e.target.value)} placeholder="Hotel Guest" />
              </Field>
            </>
          )}

          {step === 1 && (
            <>
              <fieldset className="space-y-2">
                <legend className="mb-1.5 text-label">
                  Parent interface<span className="ms-0.5 text-destructive">*</span>
                </legend>
                <p className="text-caption text-muted-foreground">
                  Only ports set aside for guest traffic can carry a guest network. The others are shown so you can
                  see why they are not offered.
                </p>
                {interfaces === null ? (
                  <div className="space-y-2" aria-busy="true">
                    <span className="sr-only">Loading interfaces</span>
                    <Skeleton className="h-16 w-full" />
                    <Skeleton className="h-16 w-full" />
                  </div>
                ) : interfaces.length === 0 ? (
                  <EmptyState icon={<Cable />} title="No interfaces discovered" hint="The appliance reported no network ports." />
                ) : (
                  <div className="grid gap-2">
                    {interfaces.map((n) => {
                      const selectable = SELECTABLE.has(n.role ?? "");
                      return (
                        <OptionCard
                          key={n.name}
                          name="parent"
                          value={n.name}
                          checked={parentInterface === n.name}
                          onChange={setParentInterface}
                          disabled={!selectable}
                          icon={<Cable />}
                          title={<span className="font-mono">{n.name}</span>}
                          badge={
                            <>
                              <Badge tone={selectable ? "info" : "default"}>{ROLE_LABEL[n.role ?? ""] ?? n.role ?? "Unknown role"}</Badge>
                              <Badge tone={n.link_state === "up" ? "ok" : "default"}>
                                {n.link_state === "up" ? "Link up" : n.link_state === "down" ? "Link down" : "Link unknown"}
                              </Badge>
                            </>
                          }
                          description={
                            <>
                              <span className="font-mono">{n.mac}</span> · MTU {n.mtu}
                              {!selectable && " · Not available for guest networks"}
                            </>
                          }
                        />
                      );
                    })}
                  </div>
                )}
              </fieldset>
              <SwitchRow
                label="VLAN tagged (802.1Q)"
                hint="Turn on when the switch port carries several networks and this one arrives tagged."
                checked={vlanTagged}
                onChange={setVlanTagged}
              />
              {vlanTagged && (
                <Field label="VLAN id" required hint="1 to 4094. Must match the VLAN your wireless controller uses for the SSID." className="max-w-48">
                  <Input type="number" min={1} max={4094} value={vlanId} onChange={(e) => setVlanId(e.target.value)} placeholder="20" />
                </Field>
              )}
            </>
          )}

          {step === 2 && (
            <>
              <Field label="Subnet (CIDR)" required hint="The whole address range of this network, e.g. 10.20.0.0/22.">
                <Input value={subnetCidr} onChange={(e) => setSubnetCidr(e.target.value)} placeholder="10.20.0.0/22" className="font-mono" />
              </Field>
              <Field
                label="Gateway IP"
                required
                hint="The appliance owns this address on the network; guests use it as their default gateway and DNS."
              >
                <Input value={gatewayIp} onChange={(e) => setGatewayIp(e.target.value)} placeholder="10.20.0.1" className="font-mono" />
              </Field>
            </>
          )}

          {step === 3 && (
            <>
              <fieldset className="space-y-2">
                <legend className="mb-1.5 text-label">
                  Address pools<span className="ms-0.5 text-destructive">*</span>
                </legend>
                {pools.map((p, i) => (
                  <div key={i} className="flex flex-wrap items-center gap-2 sm:flex-nowrap">
                    <Input
                      aria-label={`Pool ${i + 1} start`} placeholder="10.20.0.100" value={p.start_ip} className="min-w-0 flex-1 font-mono"
                      onChange={(e) => setPools((ps) => ps.map((x, k) => k === i ? { ...x, start_ip: e.target.value } : x))}
                    />
                    <span className="text-muted-foreground" aria-hidden>–</span>
                    <Input
                      aria-label={`Pool ${i + 1} end`} placeholder="10.20.3.250" value={p.end_ip} className="min-w-0 flex-1 font-mono"
                      onChange={(e) => setPools((ps) => ps.map((x, k) => k === i ? { ...x, end_ip: e.target.value } : x))}
                    />
                    <Button
                      size="icon" variant="ghost" disabled={pools.length === 1} aria-label={`Remove pool ${i + 1}`}
                      onClick={() => setPools((ps) => ps.filter((_, k) => k !== i))}
                    >
                      <X />
                    </Button>
                  </div>
                ))}
                <Button size="sm" variant="secondary" onClick={() => setPools((ps) => [...ps, { start_ip: "", end_ip: "" }])}>
                  <Plus /> Add pool
                </Button>
              </fieldset>
              <Field label="DNS for guests">
                <Select value={dnsMode} onChange={(e) => setDnsMode(e.target.value as "appliance" | "custom")}>
                  <option value="appliance">The appliance (resolve on the gateway)</option>
                  <option value="custom">Custom servers</option>
                </Select>
              </Field>
              {dnsMode === "custom" && (
                <Field label="DNS servers" required hint="Separate several with commas.">
                  <Input value={dnsServers} onChange={(e) => setDnsServers(e.target.value)} placeholder="1.1.1.1, 9.9.9.9" className="font-mono" />
                </Field>
              )}
              <Field label="Domain name">
                <Input value={domainName} onChange={(e) => setDomainName(e.target.value)} placeholder="guest.local" />
              </Field>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                <Field label="Lease time (seconds)"><Input type="number" value={leaseDefault} onChange={(e) => setLeaseDefault(e.target.value)} /></Field>
                <Field label="Shortest lease (seconds)"><Input type="number" value={leaseMin} onChange={(e) => setLeaseMin(e.target.value)} /></Field>
                <Field label="Longest lease (seconds)"><Input type="number" value={leaseMax} onChange={(e) => setLeaseMax(e.target.value)} /></Field>
              </div>
            </>
          )}

          {step === 4 && (
            <div className="space-y-4">
              <SwitchRow label="Captive portal" hint="Guests see the sign-in page before they get online." checked={captivePortal} onChange={setCaptivePortal} />
              <SwitchRow label="Internet access" hint="Guests can reach the internet once signed in." checked={internetAccess} onChange={setInternetAccess} />
              <SwitchRow label="NAT (masquerade)" hint="Guest traffic leaves through the appliance's own address." checked={nat} onChange={setNat} />
              <SwitchRow label="Client isolation" hint="Guest devices cannot reach each other." checked={clientIsolation} onChange={setClientIsolation} />
              <Callout tone="info">
                The sign-in page will be served at <span className="font-mono">{portalNote}</span> once the network is applied.
              </Callout>
            </div>
          )}

          {step === 5 && (
            <div className="space-y-5">
              <KeyValueGrid
                items={[
                  { label: "Name", value: name },
                  { label: "SSID label", value: ssidLabel || "—" },
                  { label: "Type", value: vlanTagged ? `VLAN ${vlanId}` : "Untagged" },
                  { label: "Parent interface", value: <span className="font-mono">{parentInterface}</span> },
                  { label: "Subnet", value: <span className="font-mono">{subnetCidr}</span> },
                  { label: "Gateway", value: <span className="font-mono">{gatewayIp}</span> },
                  { label: "Address pools", value: <span className="font-mono">{poolText}</span>, wide: true },
                  { label: "DNS", value: dnsMode === "custom" ? <span className="font-mono">{dnsServers}</span> : "The appliance" },
                  { label: "Domain name", value: domainName || "guest.local" },
                  { label: "Captive portal", value: onOff(captivePortal) },
                  { label: "Internet access", value: onOff(internetAccess) },
                  { label: "NAT", value: onOff(nat) },
                  { label: "Client isolation", value: onOff(clientIsolation) },
                ]}
              />
              <Callout tone="warning" title="Wireless controller action required" icon={<Radio className="size-4" />}>
                Map the &lsquo;{ssidLabel || name}&rsquo; SSID to VLAN {vlanTagged ? vlanId : "(untagged)"} on your wireless
                controller. OneGate manages the gateway, DHCP and captive portal; it does not broadcast Wi-Fi.
              </Callout>
            </div>
          )}

          {step === 6 && (
            <div className="space-y-4">
              {!created && !applied && (
                <p className="text-sm text-muted-foreground">
                  Ready to create the guest network, validate the full configuration, then apply it. After applying you
                  have a short window to keep the change; if you do not, it rolls back automatically.
                </p>
              )}
              {created && (
                <Callout tone="success" title="Guest network created">
                  <KeyValueGrid
                    className="mt-2"
                    items={[
                      { label: "Bridge", value: <span className="font-mono">{created.bridge_name}</span> },
                      { label: "Sign-in page", value: <span className="font-mono">{created.portal_url}</span> },
                    ]}
                  />
                </Callout>
              )}
              {issues && issues.length > 0 && (
                <Callout tone="danger" title="Validation failed">
                  <ValidationIssueList issues={issues} className="mt-1" />
                  <p className="mt-2">
                    The network is already created. Fix these on{" "}
                    {created ? (
                      <Link href={`/network/${created.id}`} className="font-medium underline">its own page</Link>
                    ) : "its own page"}
                    , then apply the changes from Guest networks.
                  </p>
                </Callout>
              )}
              {applied && applied.state === "pending_confirmation" && (
                <PendingChangeBanner
                  title={`Applied — revision #${applied.seq} is waiting for confirmation`}
                  description={
                    applied.message ||
                    "The new network is live now. Keep it to make it permanent; if nobody does before the timer runs out, the appliance puts the previous configuration back on its own."
                  }
                  deadline={deadline}
                  busy={acting}
                  onConfirm={onConfirm}
                  onRollback={onRollback}
                >
                  {applied.health && applied.health.length > 0 ? (
                    <div className="space-y-2">
                      <div className="text-label">Health checks</div>
                      <HealthCheckList checks={applied.health} />
                    </div>
                  ) : undefined}
                </PendingChangeBanner>
              )}
              {applied && applied.state !== "pending_confirmation" && (
                <Callout
                  tone={applied.state === "rolled_back" || applied.state === "failed" ? "danger" : "success"}
                  title={
                    applied.state === "rolled_back" ? "Apply rolled back"
                      : applied.state === "failed" ? "Apply failed"
                        : `Apply state: ${applied.state}`
                  }
                >
                  {applied.message && <p>{applied.message}</p>}
                  {applied.health && applied.health.length > 0 && <HealthCheckList checks={applied.health} className="mt-2 bg-card" />}
                </Callout>
              )}
            </div>
          )}
        </CardBody>
        <CardFooter className="justify-between">
          {step < 6 ? (
            <>
              <Button variant="ghost" onClick={back} disabled={step === 0}><ArrowLeft /> Back</Button>
              <Button onClick={next}>{step === 5 ? "Continue to apply" : "Next"} <ArrowRight /></Button>
            </>
          ) : !applied ? (
            <>
              <Button variant="ghost" onClick={back} disabled={busy}><ArrowLeft /> Back</Button>
              <Button disabled={busy} onClick={onRunApply}>
                {busy ? "Working…" : created ? "Re-validate & apply" : "Create, validate & apply"}
              </Button>
            </>
          ) : applied.state !== "pending_confirmation" ? (
            <Link href="/network" className={cn(buttonVariants({ variant: "secondary" }), "ms-auto")}>
              Back to guest networks
            </Link>
          ) : (
            <span className="text-caption text-muted-foreground">Keep or roll back the change above.</span>
          )}
        </CardFooter>
      </Card>
    </PageShell>
  );
}
