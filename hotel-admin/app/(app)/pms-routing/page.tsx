"use client";

// The Wi-Fi network decides which PMS a room number is checked against. A wrong mapping fails silently (the guest
// is checked against another property's list while every status looks healthy). A network with no mapping is
// legitimate: vouchers and accounts bypass the PMS, it just cannot offer room sign-in.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, PmsGuestNetworkRoute, PmsInterface, Whoami } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { RowActions } from "@/components/ui/row-actions";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";

import { Button } from "@/components/ui/button";
import { Select, Field } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { HelpList, HelpSection } from "@/components/help";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { Explain } from "@/components/ui/tooltip";
import { SkeletonRows } from "@/components/ui/misc";
import { canWrite } from "@/lib/roles";
import { Router, Network } from "lucide-react";

type Unmapped = { guest_network_id: string; guest_network_name?: string };

const MODE_WORDS: Record<string, string> = {
  MAPPED: "This one PMS",
  // Legacy rows: the resolver only consults the mapped interface, so this behaves exactly like MAPPED and can no longer be chosen.
  ALL_ACTIVE_INTERFACES: "This one PMS",
};

export default function PMSRoutingPage() {
  const [routes, setRoutes] = useState<PmsGuestNetworkRoute[] | null>(null);
  const [unmapped, setUnmapped] = useState<Unmapped[]>([]);
  const [interfaces, setInterfaces] = useState<PmsInterface[]>([]);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<unknown>(null);

  const [editing, setEditing] = useState<{ id: string; name: string; current?: PmsGuestNetworkRoute } | null>(null);
  const [clearing, setClearing] = useState<PmsGuestNetworkRoute | null>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const [r, i, me] = await Promise.all([
        api.get<{ routes: PmsGuestNetworkRoute[]; unmapped_guest_networks: Unmapped[] }>("/pms-routing"),
        // A row pointing at a retired interface is still shown but cannot be chosen: it would never dial.
        api.get<{ interfaces: PmsInterface[] }>("/pms-interfaces").catch(() => ({ interfaces: [] })),
        api.get<Whoami>("/auth/whoami").catch(() => null),
      ]);
      setRoutes(r.routes ?? []);
      setUnmapped(r.unmapped_guest_networks ?? []);
      setInterfaces(i.interfaces ?? []);
      setRoles(me?.roles ?? []);
      setErr(null);
    } catch (e) {
      setErr(e);
      setRoutes([]);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // edged grants pms-routing write to site_admin only; controls are hidden for other roles rather than offered and refused.
  const writable = canWrite("pms-routing", roles);
  const choosable = useMemo(
    () => interfaces.filter((i) => i.lifecycle_state !== "DECOMMISSIONED" && i.published),
    [interfaces],
  );

  async function save(mode: string, interfaceID: string) {
    if (!editing) return;
    setBusy(true); setFormErr(null);
    try {
      await api.put(`/pms-routing/${editing.id}`, { pms_interface_id: interfaceID, routing_mode: mode });
      setEditing(null);
      await load();
    } catch (e) {
      setFormErr(e);
    } finally {
      setBusy(false);
    }
  }

  async function clear() {
    if (!clearing) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/pms-routing/${clearing.guest_network_id}`);
      setClearing(null);
      await load();
    } catch (e) {
      setFormErr(e);
    } finally {
      setBusy(false);
    }
  }

  const nothingPublishable = choosable.length === 0;

  return (
    <PageShell>
      <PageHeader
        title="PMS routing"
        description="Which PMS connection each client network's room sign-ins are checked against."
        help={
          <>
            <HelpSection title="What this page sets">
              <p>
                When a guest signs in with their room number, the appliance has to know which property management
                system to check that room against. It decides from the Wi-Fi network the device is connected to —
                and that is what this page sets.
              </p>
              <p>
                Every row is one of this site&rsquo;s client networks. The site itself is fixed when the
                appliance is activated and cannot be chosen here — this page only decides which PMS connection each
                network&rsquo;s room sign-ins are checked against.
              </p>
            </HelpSection>
            <HelpSection title="Why this matters">
              <p>
                Getting this wrong does not produce an error anywhere. The guest is checked against a different
                PMS&rsquo;s guest list, no matching room is found, and they simply cannot get online — while
                the PMS connection, the networks and the packages all report healthy. If room sign-in fails on one
                Wi-Fi network but works on another, this is the first page to check.
              </p>
              <p>
                Vouchers and username-and-password accounts never involve the PMS, so a network with no mapping
                still works for those — it just cannot offer room sign-in.
              </p>
            </HelpSection>
            <HelpSection title="Scope">
              <HelpList
                items={[
                  <><strong>This one PMS</strong> checks the room against the single named connection — the normal choice for a site with one PMS.</>,
                  <>Each client network resolves against the one PMS connection it is routed to, so two extensions with the same room numbers never collide.</>,
                ]}
              />
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} />

      {nothingPublishable && interfaces.length > 0 && (
        <Callout tone="warning" title="No PMS connection is ready to be used">
          A network can only be pointed at a PMS connection that has a saved configuration. Finish configuring one
          on <Link href="/pms-interfaces" className="underline underline-offset-2">PMS connection</Link> first.
        </Callout>
      )}

      <SectionCard
        flush
        title="Networks that can offer room sign-in"
        description="A device on one of these networks is checked against the PMS named here."
      >
          {routes === null ? (
            <SkeletonRows rows={3} cols={4} />
          ) : routes.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<Router />}
              title="No network is pointed at a PMS"
              hint="Nobody at this site can sign in with a room number until at least one client network is mapped."
            />
          ) : (
            <Table label="Networks that can offer room sign-in">
              <THead>
                <TR>
                  <TH>Client network</TH>
                  <TH>Checked against</TH>
                  <TH>
                    <span className="inline-flex items-center gap-1">
                      Scope
                      <Explain>
                        The room is checked against the single PMS connection this client network is routed to.
                        That is what keeps two hotel extensions apart: each VLAN resolves against its own PMS, so
                        the same room number in both never collides.
                      </Explain>
                    </span>
                  </TH>
                  <TH><span className="sr-only">Actions</span></TH>
                </TR>
              </THead>
              <TBody>
                {routes.map((r) => (
                  <TR key={r.guest_network_id}>
                    <TD>
                      <div className="font-medium">{r.guest_network_name || "Unnamed network"}</div>
                      {!r.guest_network_name && (
                        <div className="font-mono text-2xs text-muted-foreground">{r.guest_network_id}</div>
                      )}
                    </TD>
                    <TD>
                      {/* is_default is not shown: it is per guest network and always true here (setPMSRoute replaces other mappings). */}
                      <div className="text-sm">{r.pms_interface_label || "Unnamed connection"}</div>
                    </TD>
                    <TD nowrap muted>
                      {MODE_WORDS[r.routing_mode] ?? r.routing_mode.replace(/_/g, " ").toLowerCase()}
                    </TD>
                    <TD className="w-0">
                      {writable && (
                        <RowActions
                          subject={r.guest_network_name || "Unnamed network"}
                          primary={{
                            label: "Change",
                            onSelect: () => {
                              setFormErr(null);
                              setEditing({
                                id: r.guest_network_id,
                                name: r.guest_network_name || r.guest_network_id,
                                current: r,
                              });
                            },
                          }}
                          items={[
                            { label: "Remove mapping", danger: true, onSelect: () => { setFormErr(null); setClearing(r); } },
                          ]}
                        />
                      )}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
      </SectionCard>

      <SectionCard
        flush
        title="Networks with no PMS"
        description="A guest who types a room number on these will not be recognised. Vouchers and client accounts still work."
      >
          {routes === null ? (
            <SkeletonRows rows={2} cols={2} />
          ) : unmapped.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<Network />}
              title="Every client network is pointed at a PMS"
              hint="Nothing to do here."
            />
          ) : (
            <Table label="Networks with no PMS">
              <THead>
                <TR>
                  <TH>Client network</TH>
                  <TH><span className="sr-only">Actions</span></TH>
                </TR>
              </THead>
              <TBody>
                {unmapped.map((u) => (
                  <TR key={u.guest_network_id}>
                    <TD>
                      <div className="font-medium">{u.guest_network_name || "Unnamed network"}</div>
                    </TD>
                    <TD className="w-0 whitespace-nowrap text-end">
                      {writable && (
                        <Button
                          size="xs"
                          variant="secondary"
                          disabled={nothingPublishable}
                          onClick={() => {
                            setFormErr(null);
                            setEditing({
                              id: u.guest_network_id,
                              name: u.guest_network_name || u.guest_network_id,
                            });
                          }}
                        >
                          Point at a PMS
                        </Button>
                      )}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
      </SectionCard>

      <RouteDialog
        open={editing !== null}
        network={editing}
        interfaces={choosable}
        busy={busy}
        error={formErr}
        onClose={() => setEditing(null)}
        onSave={save}
      />

      <ConfirmDialog
        open={clearing !== null}
        onOpenChange={(v) => !v && setClearing(null)}
        title="Stop offering room sign-in on this network?"
        description={
          clearing
            ? `Guests on ${clearing.guest_network_name || "this network"} will no longer be able to sign in with their room number and name. Vouchers and client accounts are unaffected, and nothing already online is disconnected.`
            : undefined
        }
        confirmLabel="Remove mapping"
        confirmVariant="danger"
        busy={busy}
        error={formErr}
        onConfirm={() => { void clear(); }}
      />
    </PageShell>
  );
}

function RouteDialog({
  open, network, interfaces, busy, error, onClose, onSave,
}: {
  open: boolean;
  network: { id: string; name: string; current?: PmsGuestNetworkRoute } | null;
  interfaces: PmsInterface[];
  busy: boolean;
  error: unknown;
  onClose: () => void;
  onSave: (mode: string, interfaceID: string) => void | Promise<void>;
}) {
  const [iface, setIface] = useState("");
  // edged accepts only MAPPED, so MAPPED is always sent; saving a legacy ALL_ACTIVE_INTERFACES row converts it.
  const legacy = !!network?.current && network.current.routing_mode !== "MAPPED";

  // Reset on each open so "Change" starts on what is configured, not the last pick.
  useEffect(() => {
    if (!open) return;
    setIface(network?.current?.pms_interface_id ?? interfaces[0]?.id ?? "");
  }, [open, network, interfaces]);

  return (
    <DialogForm
      open={open}
      onOpenChange={(v) => !v && onClose()}
      title={network?.current ? `Change the PMS for ${network.name}` : `Point ${network?.name ?? "this network"} at a PMS`}
      description="Guests on this network will have their room number checked against the connection you choose."
      submitLabel="Save mapping"
      busy={busy}
      error={error}
      disabled={!iface}
      onSubmit={() => onSave("MAPPED", iface)}
    >
      {legacy && (
        <p className="text-sm text-muted-foreground" data-testid="legacy-route-note">
          This mapping was saved by an older version as &ldquo;every active PMS&rdquo;, which always behaved as this
          one PMS. Saving stores it as this one PMS.
        </p>
      )}
      <Field
        label="Property management system"
        hint="Only connections with a saved configuration can be chosen."
      >
        <Select value={iface} onChange={(e) => setIface(e.target.value)} required>
          {interfaces.length === 0 && <option value="">No connection is ready</option>}
          {interfaces.map((i) => (
            <option key={i.id} value={i.id}>
              {i.display_label || i.id}
              {i.lifecycle_state !== "ACTIVE" ? " (not in use yet)" : ""}
            </option>
          ))}
        </Select>
      </Field>

      <Field
        label="Scope"
        hint="The room is checked against this connection only. Each client network resolves against the one PMS it is routed to — that is how two hotel extensions keep their room numbers apart."
      >
        <Select value="MAPPED" disabled aria-label="How the room is resolved">
          <option value="MAPPED">This one PMS</option>
        </Select>
      </Field>
    </DialogForm>
  );
}
