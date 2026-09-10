"use client";

// WHICH PMS EACH GUEST NETWORK IS CHECKED AGAINST.
//
// The page existed and explained nothing. Its title was "Guest network routing", its one sentence was "Which PMS
// interface each guest network is resolved against. Change these where the networks are configured", and it
// showed two tables of mostly-uuids. An operator could not tell what the page was for, what breaks without it,
// or where the "where the networks are configured" it pointed at actually was — because no such control existed.
//
// Two things changed:
//
//   1. IT SAYS WHAT IT IS FOR. When a guest types their room number, the appliance has to decide WHICH property
//      management system to check that room against. It decides by the Wi-Fi network the device is on. That is
//      the whole purpose of this screen, and getting it wrong fails silently in the worst way: the guest is
//      checked against a different property's guest list, finds no matching room, and cannot get online — while
//      every status on every other screen reports healthy.
//
//   2. IT CAN BE SET HERE. The previous "change it where the networks are configured" was not true: the guest
//      network API has no PMS field, so the mapping could be set nowhere in the product and existed only as a
//      row written by test fixtures. edged has had PUT/DELETE on this resource since; this screen now uses them.
//
// Rooms are only one way to sign in. Vouchers and guest accounts do not involve the PMS at all, so a network with
// no mapping is a legitimate configuration — it just cannot offer room sign-in. The page says that rather than
// flagging it as broken.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, PmsGuestNetworkRoute, PmsInterface, Whoami } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select, Field } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { Explain } from "@/components/ui/tooltip";
import { SkeletonRows } from "@/components/ui/misc";
import { canWrite } from "@/lib/roles";
import { Router, Network } from "lucide-react";

type Unmapped = { guest_network_id: string; guest_network_name?: string };

const MODE_WORDS: Record<string, string> = {
  MAPPED: "This one PMS",
  ALL_ACTIVE_INTERFACES: "Every active PMS",
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
        // The pickable interfaces. A routing row pointing at a retired interface is still shown, but it cannot be
        // chosen: offering it would let an operator configure a connection that will never dial.
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

  // edged grants pms-routing WRITE to site_admin only — every other role reads it. The controls are therefore
  // hidden rather than offered and refused: a Change button that always 403s is worse than no Change button.
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
        eyebrow="Property management system"
        title="Which PMS each network checks"
        description="When a guest signs in with their room number, the appliance has to know which property management system to check that room against. It decides from the Wi-Fi network the device is connected to — and that is what this page sets."
      />

      <ErrorBanner err={err} />

      <Callout tone="info" title="Why this matters">
        Getting this wrong does not produce an error anywhere. The guest is checked against a different
        property&rsquo;s guest list, no matching room is found, and they simply cannot get online — while the PMS
        connection, the networks and the packages all report healthy. If room sign-in fails on one Wi-Fi network
        but works on another, this is the first page to check.
        <div className="mt-2 text-xs">
          Vouchers and username-and-password accounts never involve the PMS, so a network with no mapping still
          works for those — it just cannot offer room sign-in.
        </div>
      </Callout>

      {nothingPublishable && interfaces.length > 0 && (
        <Callout tone="warning" title="No PMS connection is ready to be used">
          A network can only be pointed at a PMS connection that has a saved configuration. Finish configuring one
          on <Link href="/pms-interfaces" className="underline underline-offset-2">PMS connection</Link> first.
        </Callout>
      )}

      <Card>
        <CardHeader>
          <div>
            <CardTitle>Networks that can offer room sign-in</CardTitle>
            <p className="mt-0.5 text-xs text-muted-foreground">
              A device on one of these networks is checked against the PMS named here.
            </p>
          </div>
        </CardHeader>
        <CardBody className="p-0">
          {routes === null ? (
            <SkeletonRows rows={3} cols={4} />
          ) : routes.length === 0 ? (
            <EmptyState
              icon={<Router />}
              title="No network is pointed at a PMS"
              hint="Nobody at this property can sign in with a room number until at least one guest network is mapped."
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Guest network</TH>
                  <TH>Checked against</TH>
                  <TH>
                    <span className="inline-flex items-center gap-1">
                      Scope
                      <Explain>
                        <strong>This one PMS</strong> checks the room against the single named connection — the
                        normal choice for a property with one PMS. <strong>Every active PMS</strong> tries all of
                        them, which only makes sense where one appliance serves several properties.
                      </Explain>
                    </span>
                  </TH>
                  <TH />
                </TR>
              </THead>
              <tbody>
                {routes.map((r) => (
                  <TR key={r.guest_network_id}>
                    <TD>
                      <div className="font-medium">{r.guest_network_name || "Unnamed network"}</div>
                      {!r.guest_network_name && (
                        <div className="font-mono text-2xs text-muted-foreground">{r.guest_network_id}</div>
                      )}
                    </TD>
                    <TD>
                      <div className="text-sm">{r.pms_interface_label || "Unnamed connection"}</div>
                      {r.is_default && <Badge tone="info" className="mt-0.5">Site default</Badge>}
                    </TD>
                    <TD className="text-sm text-muted-foreground">
                      {MODE_WORDS[r.routing_mode] ?? r.routing_mode.replace(/_/g, " ").toLowerCase()}
                    </TD>
                    <TD className="whitespace-nowrap text-right">
                      {writable && (
                        <>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => {
                              setFormErr(null);
                              setEditing({
                                id: r.guest_network_id,
                                name: r.guest_network_name || r.guest_network_id,
                                current: r,
                              });
                            }}
                          >
                            Change
                          </Button>
                          <Button size="sm" variant="ghost" onClick={() => { setFormErr(null); setClearing(r); }}>
                            Remove
                          </Button>
                        </>
                      )}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <div>
            <CardTitle>Networks with no PMS</CardTitle>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Room sign-in is not offered on these. Vouchers and guest accounts still work.
            </p>
          </div>
        </CardHeader>
        <CardBody className={unmapped.length ? "p-0" : undefined}>
          {routes === null ? (
            <SkeletonRows rows={2} cols={2} />
          ) : unmapped.length === 0 ? (
            <EmptyState
              icon={<Network />}
              title="Every guest network is pointed at a PMS"
              hint="Nothing to do here."
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Guest network</TH>
                  <TH />
                </TR>
              </THead>
              <tbody>
                {unmapped.map((u) => (
                  <TR key={u.guest_network_id}>
                    <TD>
                      <div className="font-medium">{u.guest_network_name || "Unnamed network"}</div>
                      <div className="text-xs text-muted-foreground">
                        A guest on this network who types a room number will not be recognised.
                      </div>
                    </TD>
                    <TD className="text-right">
                      {writable && (
                        <Button
                          size="sm"
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
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

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
            ? `Guests on ${clearing.guest_network_name || "this network"} will no longer be able to sign in with their room number and name. Vouchers and guest accounts are unaffected, and nothing already online is disconnected.`
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
  const [mode, setMode] = useState("MAPPED");

  // Reset from the row each time the dialog opens, so "Change" starts on what is configured and "Point at a PMS"
  // starts on the only sensible default rather than on whatever was picked last time.
  useEffect(() => {
    if (!open) return;
    setIface(network?.current?.pms_interface_id ?? interfaces[0]?.id ?? "");
    setMode(network?.current?.routing_mode ?? "MAPPED");
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
      onSubmit={() => onSave(mode, iface)}
    >
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
        hint={
          mode === "MAPPED"
            ? "The room is checked against this connection only. This is what a single-property appliance wants."
            : "The room is tried against every PMS connection that is in use. Only correct where one appliance serves more than one property."
        }
      >
        <Select value={mode} onChange={(e) => setMode(e.target.value)}>
          <option value="MAPPED">This one PMS</option>
          <option value="ALL_ACTIVE_INTERFACES">Every active PMS</option>
        </Select>
      </Field>
    </DialogForm>
  );
}
