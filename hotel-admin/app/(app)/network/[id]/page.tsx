"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import {
  api, ListResp, Pool,
  GuestNetwork, GuestNetworkStatus, Reservation, GuestNetworkInput,
} from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button, buttonVariants } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { PageShell, PageHeader } from "@/components/ui/page";
import { KeyValueGrid } from "@/components/ui/data";
import { MonoId, Skeleton, SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { ArrowLeft, Network, Pencil, Plus, Save, Trash2, X, Pin } from "lucide-react";
import { cn, errMsg } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";
import {
  AddReservationDialog, EditReservationDialog, RemoveReservationDialog, SwitchRow,
  DhcpModeBadge, networkTypeLabel, useNetworkAccess,
} from "@/components/network/shared";

const FORM_ID = "guest-network-settings";

export default function EditGuestNetworkPage() {
  const { id } = useParams<{ id: string }>();
  const { known, writable } = useNetworkAccess();
  const [net, setNet] = useState<GuestNetwork | null>(null);
  const [status, setStatus] = useState<GuestNetworkStatus | null>(null);
  const [reservations, setReservations] = useState<Reservation[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  // editable form state
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [ssidLabel, setSsidLabel] = useState("");
  const [subnetCidr, setSubnetCidr] = useState("");
  const [gatewayIp, setGatewayIp] = useState("");
  const [pools, setPools] = useState<Pool[]>([]);
  const [dnsMode, setDnsMode] = useState("appliance");
  const [dnsServers, setDnsServers] = useState("");
  const [domainName, setDomainName] = useState("");
  const [leaseDefault, setLeaseDefault] = useState("");
  const [leaseMin, setLeaseMin] = useState("");
  const [leaseMax, setLeaseMax] = useState("");
  const [captivePortal, setCaptivePortal] = useState(true);
  const [internetAccess, setInternetAccess] = useState(true);
  const [nat, setNat] = useState(true);
  const [clientIsolation, setClientIsolation] = useState(false);

  // reservation dialogs
  const [adding, setAdding] = useState(false);
  const [editRes, setEditRes] = useState<Reservation | null>(null);
  const [removing, setRemoving] = useState<Reservation | null>(null);

  function hydrate(g: GuestNetwork) {
    setNet(g);
    setName(g.name);
    setDescription(g.description ?? "");
    setSsidLabel(g.ssid_label ?? "");
    setSubnetCidr(g.subnet_cidr);
    setGatewayIp(g.gateway_ip);
    setPools(g.pools && g.pools.length ? g.pools : [{ start_ip: "", end_ip: "" }]);
    setDnsMode(g.dns_mode);
    setDnsServers((g.dns_servers ?? []).join(", "));
    setDomainName(g.domain_name);
    setLeaseDefault(String(g.lease_default_seconds));
    setLeaseMin(String(g.lease_min_seconds));
    setLeaseMax(String(g.lease_max_seconds));
    setCaptivePortal(g.captive_portal_enabled);
    setInternetAccess(g.internet_access_enabled);
    setNat(g.nat_enabled);
    setClientIsolation(g.client_isolation_enabled);
  }

  async function loadReservations() {
    try {
      const r = await api.get<ListResp<Reservation>>(`/network/dhcp/reservations?guest_network_id=${id}`);
      setReservations(r.data ?? []);
    } catch (e) { setErr(errMsg(e)); }
  }

  async function loadStatus() {
    try { setStatus(await api.get<GuestNetworkStatus>(`/network/guest-networks/${id}/status`)); }
    catch { /* status optional */ }
  }

  useEffect(() => {
    if (!id) return;
    api.get<GuestNetwork>(`/network/guest-networks/${id}`).then(hydrate).catch((e) => setErr(errMsg(e)));
    loadStatus();
    loadReservations();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  async function onSave() {
    setBusy(true); setErr(null); setSaved(false);
    const body: GuestNetworkInput = {
      name: name.trim(),
      description: description.trim() || undefined,
      ssid_label: ssidLabel.trim() || undefined,
      gateway_ip: gatewayIp.trim(),
      subnet_cidr: subnetCidr.trim(),
      dhcp_mode: net?.dhcp_mode ?? "local",
      dns_mode: dnsMode,
      dns_servers: dnsMode === "custom" ? dnsServers.split(",").map((s) => s.trim()).filter(Boolean) : undefined,
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
    try {
      await api.put(`/network/guest-networks/${id}`, body);
      setSaved(true);
      toast.success("Saved", "Apply changes from Guest networks to put it live.");
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  const ro = !writable;
  const enabled = status?.enabled ?? net?.enabled;

  const backLink = (
    <Link href="/network" className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "-ms-3 self-start")}>
      <ArrowLeft /> Guest networks
    </Link>
  );

  return (
    <PageShell>
      {backLink}
      <PageHeader
        icon={<Network />}
        eyebrow="Networking · Guest networks"
        title={net?.name || "Guest network"}
        description={
          net ? (
            <span className="inline-flex flex-wrap items-center gap-1.5">
              <Badge tone={net.network_type === "vlan" ? "info" : "default"}>{networkTypeLabel(net)}</Badge>
              {enabled ? <Badge tone="ok" dot>Enabled</Badge> : <Badge tone="default">Disabled</Badge>}
            </span>
          ) : "Loading the network…"
        }
        helpTitle="Guest network"
        help={
          <>
            <HelpSection title="Saving is staging">
              <p>
                <strong>Save changes</strong> stages the new settings. Guests keep the previous settings until you
                validate and apply the changes from <strong>Guest networks</strong>.
              </p>
            </HelpSection>
            <HelpSection title="What cannot be edited">
              <p>
                The type, VLAN and parent interface are fixed when the network is created. To change them, delete the
                network and create a new one.
              </p>
            </HelpSection>
            <HelpSection title="DHCP reservations">
              <HelpList
                items={[
                  "A reservation pins a device (by MAC address) to a fixed address inside this network.",
                  "Reserved addresses must sit inside the network's subnet.",
                ]}
              />
            </HelpSection>
          </>
        }
        actions={writable && net && (
          <Button type="submit" form={FORM_ID} disabled={busy}>
            <Save /> {busy ? "Saving…" : "Save changes"}
          </Button>
        )}
      />

      {known && !writable && <ReadOnlyNotice>Your role can view this guest network but not change it.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      {saved && (
        <Callout tone="success" title="Saved — not applied yet">
          Guests are still on the previous settings.{" "}
          <Link href="/network" className="font-medium underline">Go to Guest networks</Link> to validate and apply.
        </Callout>
      )}

      {!net ? (
        err ? null : (
          <div className="space-y-4" aria-busy="true">
            <span className="sr-only">Loading</span>
            <Skeleton className="h-40 w-full" />
            <Skeleton className="h-80 w-full" />
          </div>
        )
      ) : (
        <>
          {/* read-only topology + status */}
          <Card>
            <CardHeader>
              <div className="space-y-1">
                <CardTitle>Status &amp; topology</CardTitle>
                <CardDescription>Fixed when the network was created.</CardDescription>
              </div>
            </CardHeader>
            <CardBody>
              <KeyValueGrid
                columns={3}
                items={[
                  { label: "Type", value: networkTypeLabel(net) },
                  { label: "Parent interface", value: <span className="font-mono">{net.parent_interface}</span> },
                  { label: "Bridge", value: <span className="font-mono">{net.bridge_name}</span> },
                  { label: "Sign-in page URL", value: net.portal_url ? <span className="font-mono break-all">{net.portal_url}</span> : "—" },
                  { label: "Status", value: enabled ? <Badge tone="ok" dot>Enabled</Badge> : <Badge tone="default">Disabled</Badge> },
                  { label: "Devices connected", value: <span className="tabular">{String(status?.active_clients ?? "—")}</span> },
                  { label: "DHCP", value: <DhcpModeBadge mode={net.dhcp_mode} /> },
                  { label: "Network ID", value: <MonoId value={net.id} title="Network ID" /> },
                ]}
              />
            </CardBody>
          </Card>

          {/* editable settings */}
          <form
            id={FORM_ID}
            onSubmit={(e) => { e.preventDefault(); if (writable) void onSave(); }}
            className="grid gap-5 lg:grid-cols-2"
          >
            <Card>
              <CardHeader><CardTitle>Identity</CardTitle></CardHeader>
              <CardBody className="space-y-4">
                <Field label="Name" required><Input value={name} onChange={(e) => setName(e.target.value)} disabled={ro} /></Field>
                <Field label="SSID label" hint="For reference: the SSID your wireless controller maps to this network.">
                  <Input value={ssidLabel} onChange={(e) => setSsidLabel(e.target.value)} disabled={ro} />
                </Field>
                <Field label="Description"><Input value={description} onChange={(e) => setDescription(e.target.value)} disabled={ro} /></Field>
              </CardBody>
            </Card>

            <Card>
              <CardHeader><CardTitle>Addressing</CardTitle></CardHeader>
              <CardBody className="space-y-4">
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <Field label="Subnet (CIDR)" required>
                    <Input value={subnetCidr} onChange={(e) => setSubnetCidr(e.target.value)} disabled={ro} className="font-mono" />
                  </Field>
                  <Field label="Gateway IP" required>
                    <Input value={gatewayIp} onChange={(e) => setGatewayIp(e.target.value)} disabled={ro} className="font-mono" />
                  </Field>
                </div>
                <fieldset className="space-y-2">
                  <legend className="mb-1.5 text-label">Address pools</legend>
                  {pools.map((p, i) => (
                    <div key={i} className="flex flex-wrap items-center gap-2 sm:flex-nowrap">
                      <Input
                        aria-label={`Pool ${i + 1} start`} placeholder="start" value={p.start_ip} disabled={ro} className="min-w-0 flex-1 font-mono"
                        onChange={(e) => setPools((ps) => ps.map((x, k) => k === i ? { ...x, start_ip: e.target.value } : x))}
                      />
                      <span className="text-muted-foreground" aria-hidden>–</span>
                      <Input
                        aria-label={`Pool ${i + 1} end`} placeholder="end" value={p.end_ip} disabled={ro} className="min-w-0 flex-1 font-mono"
                        onChange={(e) => setPools((ps) => ps.map((x, k) => k === i ? { ...x, end_ip: e.target.value } : x))}
                      />
                      {writable && (
                        <Button
                          size="icon" variant="ghost" disabled={pools.length === 1} aria-label={`Remove pool ${i + 1}`}
                          onClick={() => setPools((ps) => ps.filter((_, k) => k !== i))}
                        >
                          <X />
                        </Button>
                      )}
                    </div>
                  ))}
                  {writable && (
                    <Button size="sm" variant="secondary" onClick={() => setPools((ps) => [...ps, { start_ip: "", end_ip: "" }])}>
                      <Plus /> Add pool
                    </Button>
                  )}
                </fieldset>
              </CardBody>
            </Card>

            <Card>
              <CardHeader><CardTitle>DNS &amp; leases</CardTitle></CardHeader>
              <CardBody className="space-y-4">
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <Field label="DNS for guests">
                    <Select value={dnsMode} onChange={(e) => setDnsMode(e.target.value)} disabled={ro}>
                      <option value="appliance">The appliance</option>
                      <option value="custom">Custom servers</option>
                    </Select>
                  </Field>
                  <Field label="Domain name">
                    <Input value={domainName} onChange={(e) => setDomainName(e.target.value)} disabled={ro} />
                  </Field>
                </div>
                {dnsMode === "custom" && (
                  <Field label="DNS servers" hint="Separate several with commas.">
                    <Input value={dnsServers} onChange={(e) => setDnsServers(e.target.value)} disabled={ro} className="font-mono" />
                  </Field>
                )}
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                  <Field label="Lease time (s)"><Input type="number" value={leaseDefault} onChange={(e) => setLeaseDefault(e.target.value)} disabled={ro} /></Field>
                  <Field label="Shortest (s)"><Input type="number" value={leaseMin} onChange={(e) => setLeaseMin(e.target.value)} disabled={ro} /></Field>
                  <Field label="Longest (s)"><Input type="number" value={leaseMax} onChange={(e) => setLeaseMax(e.target.value)} disabled={ro} /></Field>
                </div>
              </CardBody>
            </Card>

            <Card>
              <CardHeader><CardTitle>Guest access</CardTitle></CardHeader>
              <CardBody className="space-y-4">
                <SwitchRow label="Captive portal" hint="Guests see the sign-in page before they get online." checked={captivePortal} onChange={setCaptivePortal} disabled={ro} />
                <SwitchRow label="Internet access" hint="Guests can reach the internet once signed in." checked={internetAccess} onChange={setInternetAccess} disabled={ro} />
                <SwitchRow label="NAT (masquerade)" hint="Guest traffic leaves through the appliance's own address." checked={nat} onChange={setNat} disabled={ro} />
                <SwitchRow label="Client isolation" hint="Guest devices cannot reach each other." checked={clientIsolation} onChange={setClientIsolation} disabled={ro} />
              </CardBody>
              {writable && (
                <CardFooter className="justify-end">
                  <Button type="submit" disabled={busy}><Save /> {busy ? "Saving…" : "Save changes"}</Button>
                </CardFooter>
              )}
            </Card>
          </form>

          {/* reservations */}
          <Card>
            <CardHeader>
              <div className="space-y-1">
                <CardTitle>DHCP reservations</CardTitle>
                <CardDescription>Devices that always get the same address on this network.</CardDescription>
              </div>
              {writable && (
                <Button variant="secondary" size="sm" onClick={() => setAdding(true)}><Plus /> Add reservation</Button>
              )}
            </CardHeader>
            {reservations === null ? (
              <SkeletonRows rows={2} cols={4} />
            ) : reservations.length === 0 ? (
              <EmptyState
                icon={<Pin />}
                title="No reservations"
                hint="Pin a device to a fixed address inside this network."
                action={writable ? <Button size="sm" onClick={() => setAdding(true)}><Plus /> Add reservation</Button> : undefined}
              />
            ) : (
              <Table>
                <THead>
                  <TR>
                    <TH>MAC address</TH><TH>Reserved IP</TH>
                    <TH className="hidden sm:table-cell">Hostname</TH><TH>Status</TH>
                    {writable && <TH><span className="sr-only">Actions</span></TH>}
                  </TR>
                </THead>
                <TBody>
                  {reservations.map((r) => (
                    <TR key={r.id}>
                      <TD className="font-mono text-xs">{r.mac}</TD>
                      <TD className="font-mono text-xs">{r.reserved_ip}</TD>
                      <TD className="hidden text-muted-foreground sm:table-cell">{r.hostname || "—"}</TD>
                      <TD>{r.enabled ? <Badge tone="ok">Enabled</Badge> : <Badge tone="default">Disabled</Badge>}</TD>
                      {writable && (
                        <TD className="whitespace-nowrap text-end">
                          <Button size="icon-sm" variant="ghost" aria-label={`Edit reservation ${r.reserved_ip}`} onClick={() => setEditRes(r)}><Pencil /></Button>
                          <Button size="icon-sm" variant="ghost" aria-label={`Remove reservation ${r.reserved_ip}`} onClick={() => setRemoving(r)}><Trash2 /></Button>
                        </TD>
                      )}
                    </TR>
                  ))}
                </TBody>
              </Table>
            )}
          </Card>
        </>
      )}

      {writable && (
        <>
          <AddReservationDialog open={adding} onOpenChange={setAdding} networkId={id} onSaved={() => { toast.success("Reservation added"); loadReservations(); }} />
          <EditReservationDialog reservation={editRes} onClose={() => setEditRes(null)} onSaved={() => { toast.success("Reservation saved"); loadReservations(); }} />
          <RemoveReservationDialog reservation={removing} onClose={() => setRemoving(null)} onRemoved={() => { toast.success("Reservation removed"); loadReservations(); }} />
        </>
      )}
    </PageShell>
  );
}
