"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import {
  api, ListResp, Pool,
  GuestNetwork, GuestNetworkStatus, Reservation, GuestNetworkInput,
} from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Button, LinkButton } from "@/components/ui/button";
import { Field, FieldGrid, Input, InputGroup, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { PageShell, PageHeader } from "@/components/ui/page";
import { DescriptionList } from "@/components/ui/data";
import { MonoId, Skeleton, SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { ArrowLeft, Plus, Save, X, Pin, Shuffle } from "lucide-react";
import { HelpList, HelpSection } from "@/components/help";
import { errMsg } from "@/lib/utils";
import { ReplaceTopologyDialog, type ReplaceResult } from "@/components/network/replace-topology-dialog";
import { DiagnosticsPanel } from "@/components/network/status";
import {
  AddReservationDialog, EditReservationDialog, RemoveReservationDialog, ReservationTable, SwitchRow,
  DhcpModeBadge, EnabledBadge, networkTypeLabel, useNetworkAccess,
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
  const [replacing, setReplacing] = useState(false);
  const [replaced, setReplaced] = useState<ReplaceResult | null>(null);
  const toast = useToast();

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
      toast.success("Saved", "Apply changes from Client networks to put it live.");
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  const ro = !writable;
  const enabled = status?.enabled ?? net?.enabled;
  const saveButton = (
    <Button type="submit" form={FORM_ID} disabled={busy}>
      <Save /> {busy ? "Saving…" : "Save changes"}
    </Button>
  );

  return (
    <PageShell>
      <LinkButton href="/network" variant="ghost" size="sm" className="-ms-2.5 self-start">
        <ArrowLeft className="rtl:rotate-180" /> Client networks
      </LinkButton>
      <PageHeader
        title={net?.name || "Client network"}
        meta={net && (
          <>
            <Badge tone={net.network_type === "vlan" ? "info" : "default"}>{networkTypeLabel(net)}</Badge>
            <EnabledBadge enabled={enabled} />
          </>
        )}
        description={net ? undefined : "Loading the network…"}
        helpTitle="Client network"
        help={
          <>
            <HelpSection title="Saving is staging">
              <p>
                <strong>Save changes</strong> stages the new settings. Clients keep the previous settings until you
                validate and apply the changes from <strong>Client networks</strong>.
              </p>
            </HelpSection>
            <HelpSection title="What cannot be edited">
              <p>
                The type, VLAN and port cannot be edited in place. Use <strong>Change VLAN or port</strong>: it creates a
                replacement with the new topology that keeps this network&rsquo;s addressing, DHCP pools, reservations,
                settings and PMS route, and disables this one. Nothing changes until you apply and confirm.
              </p>
            </HelpSection>
            <HelpSection title="Client access switches">
              <HelpList
                items={[
                  <><strong>Captive portal</strong> — with it off, nobody on this network can sign in, so it has no internet access (only allowed sites).</>,
                  <><strong>Client isolation</strong> — the appliance drops traffic between client devices that passes through it. Devices on the same access point or switch port also need the access point&rsquo;s own client isolation.</>,
                ]}
              />
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
        actions={writable && net && saveButton}
      />

      {known && !writable && <ReadOnlyNotice>Your role can view this client network but not change it.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      {saved && (
        <Callout tone="success" title="Saved — not applied yet">
          Clients are still on the previous settings.{" "}
          <Link href="/network" className="font-medium underline">Go to Client networks</Link> to validate and apply.
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
          {replaced && (
            <Callout tone="success" title="Change recorded — nothing has changed yet">
              <p>
                This network is still serving its clients. When you{" "}
                <Link href="/network" className="font-medium underline">apply the configuration</Link> and confirm it, the
                replacement takes over carrying {replaced.carries.pools} DHCP pool(s),{" "}
                {replaced.carries.reservations} reservation(s) and {replaced.carries.pms_routes} PMS route(s).
              </p>
              {!!replaced.packages?.length && (
                <p className="mt-1">
                  Internet packages limited to this network ({replaced.packages.join(", ")}) are moved onto the
                  replacement automatically when you confirm. If one of them cannot be moved, the change is not
                  kept and you are told which package and why.
                </p>
              )}
              {/* Replacement preserves network identity, so printed vouchers remain valid. */}
              <p className="mt-1">
                Vouchers already printed for those packages keep working. The replacement counts as this same
                client network, so there is nothing to reissue.
              </p>
            </Callout>
          )}

          <SectionCard
            title="Status & topology"
            description="Not editable in place — change it with a replacement."
            actions={writable && enabled && !replaced && (
              <Button variant="secondary" size="sm" onClick={() => setReplacing(true)}>
                <Shuffle /> Change VLAN or port
              </Button>
            )}
          >
            <DescriptionList
              layout="stacked"
              columns={3}
              items={[
                { label: "Type", value: networkTypeLabel(net) },
                { label: "Parent interface", value: <span className="font-mono">{net.parent_interface}</span> },
                { label: "Bridge", value: <span className="font-mono">{net.bridge_name}</span> },
                { label: "Sign-in page URL", value: net.portal_url ? <span className="break-all font-mono">{net.portal_url}</span> : "—" },
                { label: "Status", value: <EnabledBadge enabled={enabled} /> },
                { label: "Devices connected", value: <span className="tabular">{String(status?.active_clients ?? "—")}</span> },
                { label: "DHCP", value: <DhcpModeBadge mode={net.dhcp_mode} /> },
                { label: "Network ID", value: <MonoId value={net.id} title="Network ID" /> },
              ]}
            />
          </SectionCard>

          <DiagnosticsPanel networkId={net.id} />

          {replacing && (
            <ReplaceTopologyDialog
              open={replacing}
              onOpenChange={setReplacing}
              network={{ id: net.id, name: net.name, network_type: net.network_type, parent_interface: net.parent_interface,
                vlan_id: net.vlan_id ?? null, subnet_cidr: net.subnet_cidr, gateway_ip: net.gateway_ip }}
              onReplaced={(r) => { setReplacing(false); setReplaced(r); void loadStatus(); }}
            />
          )}

          <form
            id={FORM_ID}
            onSubmit={(e) => { e.preventDefault(); if (writable) void onSave(); }}
            className="grid gap-5 lg:grid-cols-2"
          >
            <SectionCard title="Identity" bodyClassName="space-y-4">
              <Field label="Name" required><Input value={name} onChange={(e) => setName(e.target.value)} disabled={ro} /></Field>
              <Field label="SSID label" optional hint="The SSID your wireless controller maps to this network.">
                <Input value={ssidLabel} onChange={(e) => setSsidLabel(e.target.value)} disabled={ro} />
              </Field>
              <Field label="Description" optional><Input value={description} onChange={(e) => setDescription(e.target.value)} disabled={ro} /></Field>
            </SectionCard>

            <SectionCard title="Addressing" bodyClassName="space-y-4">
              <FieldGrid cols={2}>
                <Field label="Subnet (CIDR)" required>
                  <Input value={subnetCidr} onChange={(e) => setSubnetCidr(e.target.value)} disabled={ro} className="font-mono" />
                </Field>
                <Field label="Gateway IP" required>
                  <Input value={gatewayIp} onChange={(e) => setGatewayIp(e.target.value)} disabled={ro} className="font-mono" />
                </Field>
              </FieldGrid>
              <fieldset className="space-y-2">
                <legend className="mb-1.5 text-label">Address pools</legend>
                {pools.map((p, i) => (
                  <div key={i} className="flex items-center gap-2">
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
            </SectionCard>

            <SectionCard title="DNS & leases" bodyClassName="space-y-4">
              <FieldGrid cols={2}>
                <Field label="DNS for clients">
                  <Select value={dnsMode} onChange={(e) => setDnsMode(e.target.value)} disabled={ro}>
                    <option value="appliance">The appliance</option>
                    <option value="custom">Custom servers</option>
                  </Select>
                </Field>
                <Field label="Domain name">
                  <Input value={domainName} onChange={(e) => setDomainName(e.target.value)} disabled={ro} />
                </Field>
              </FieldGrid>
              {dnsMode === "custom" && (
                <Field label="DNS servers" hint="Separate several with commas.">
                  <Input value={dnsServers} onChange={(e) => setDnsServers(e.target.value)} disabled={ro} className="font-mono" />
                </Field>
              )}
              <FieldGrid cols={3}>
                <Field label="Lease time">
                  <InputGroup suffix="s"><Input type="number" value={leaseDefault} onChange={(e) => setLeaseDefault(e.target.value)} disabled={ro} /></InputGroup>
                </Field>
                <Field label="Shortest">
                  <InputGroup suffix="s"><Input type="number" value={leaseMin} onChange={(e) => setLeaseMin(e.target.value)} disabled={ro} /></InputGroup>
                </Field>
                <Field label="Longest">
                  <InputGroup suffix="s"><Input type="number" value={leaseMax} onChange={(e) => setLeaseMax(e.target.value)} disabled={ro} /></InputGroup>
                </Field>
              </FieldGrid>
            </SectionCard>

            <SectionCard title="Client access" bodyClassName="space-y-4" footer={writable ? saveButton : undefined}>
              <SwitchRow label="Captive portal" hint="Clients see the sign-in page before they get online. Off means no internet on this network." checked={captivePortal} onChange={setCaptivePortal} disabled={ro} />
              <SwitchRow label="Internet access" hint="Clients can reach the internet once signed in." checked={internetAccess} onChange={setInternetAccess} disabled={ro} />
              <SwitchRow label="NAT (masquerade)" hint="Client traffic leaves through the appliance's own address." checked={nat} onChange={setNat} disabled={ro} />
              <SwitchRow label="Client isolation" hint="Drops device-to-device traffic that passes through the appliance." checked={clientIsolation} onChange={setClientIsolation} disabled={ro} />
            </SectionCard>
          </form>

          <SectionCard
            flush
            title="DHCP reservations"
            description="Devices that always get the same address on this network."
            actions={writable && (
              <Button variant="secondary" size="sm" onClick={() => setAdding(true)}><Plus /> Add reservation</Button>
            )}
          >
            {reservations === null ? (
              <SkeletonRows rows={2} cols={4} />
            ) : reservations.length === 0 ? (
              <EmptyState size="sm" icon={<Pin />} title="No reservations" hint="Pin a device to a fixed address inside this network." />
            ) : (
              <ReservationTable reservations={reservations} writable={writable} onEdit={setEditRes} onRemove={setRemoving} />
            )}
          </SectionCard>
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
