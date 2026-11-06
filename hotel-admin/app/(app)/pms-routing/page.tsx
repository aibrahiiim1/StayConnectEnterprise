"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { AlertTriangle, RefreshCw, Router } from "lucide-react";
import { api, PmsInterface } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { RowActions } from "@/components/ui/row-actions";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";
import { HelpList, HelpSection } from "@/components/help";
import { SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { ClearRoutingDialog, RoutingEditor, type ChoosableInterface } from "@/components/pms/routing-editor";
import { RoomSignInReadinessCallout, useRoomSignInReadiness } from "@/components/room-sign-in-readiness";
import { canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { cn } from "@/lib/utils";
import { POLICY_WORDS, STATE_WORDS, type RoutingState } from "@/lib/api/pms-routing";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

export default function PMSRoutingPage() {
  const roles = useOperatorRoles();
  const [policies, setPolicies] = useState<RoutingState[] | null>(null);
  const [policiesFailed, setPoliciesFailed] = useState(false);
  const [interfaces, setInterfaces] = useState<PmsInterface[] | null>(null);
  const [interfacesFailed, setInterfacesFailed] = useState(false);
  const [loading, setLoading] = useState(false);
  const { readiness, assessment, reload: reloadReadiness } = useRoomSignInReadiness();

  const [editing, setEditing] = useState<{ id: string; name: string } | null>(null);
  const [clearing, setClearing] = useState<RoutingState | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    const [r, i] = await Promise.allSettled([
      api.get<{ policies: RoutingState[] }>("/pms-routing"),
      api.get<{ interfaces: PmsInterface[] }>("/pms-interfaces"),
    ]);
    if (r.status === "fulfilled") {
      setPolicies(r.value.policies ?? []);
      setPoliciesFailed(false);
    } else {
      setPoliciesFailed(true);
    }
    if (i.status === "fulfilled") {
      setInterfaces(i.value.interfaces ?? []);
      setInterfacesFailed(false);
    } else {
      setInterfacesFailed(true);
    }
    setLoading(false);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const refresh = useCallback(async () => {
    await Promise.all([load(), reloadReadiness()]);
  }, [load, reloadReadiness]);

  const writable = roles !== null && canWrite("pms-routing", roles);
  const choosable: ChoosableInterface[] = useMemo(
    () => (interfaces ?? []).filter((i) => i.lifecycle_state !== "DECOMMISSIONED").map((i) => ({
      id: i.id, label: i.display_label || "Unnamed PMS connection", lifecycle: i.lifecycle_state, published: i.published,
    })),
    [interfaces],
  );
  const stateOf = useMemo(() => new Map((assessment?.networks ?? []).map((n) => [n.network_id, n])), [assessment]);
  const readyCount = choosable.filter((c) => c.lifecycle === "ACTIVE" && c.published).length;
  const loadFailed = policiesFailed && policies === null;
  const spin = (on: boolean) => cn(on && "animate-spin motion-reduce:animate-none");

  return (
    <PageShell>
      <PageHeader
        title="PMS routing"
        description="How room sign-in works on each client network, and which PMS connections it checks."
        actions={policies ? (
          <Button size="sm" variant="secondary" onClick={() => void refresh()} disabled={loading}>
            <RefreshCw className={spin(loading)} /> Refresh
          </Button>
        ) : undefined}
        help={
          <>
            <HelpSection title="What this page sets">
              <p>
                When a guest signs in with their room number, the appliance has to know which property management system to check the room against. It decides from the
                client network the device is on, and each network has its own choice.
              </p>
              <HelpList
                items={[
                  <><strong>No room sign-in</strong> — the network is deliberately PMS-free (vouchers, accounts, other methods). It is never reported as a fault.</>,
                  <><strong>One dedicated PMS</strong> — the room is checked against that one connection. Several networks may use the same PMS.</>,
                  <><strong>A shared strict set</strong> — two or three independent connections serve one network. Each keeps its own rooms and guests; they are never merged. A guest is admitted only when exactly one finds them and every other clearly does not; if one cannot answer, no guest on that network is admitted, and the guest is never asked which PMS they belong to.</>,
                  <><strong>Not decided</strong> — nobody chose, so room sign-in is not offered on that network until someone does.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Why you preview first">
              <p>
                A wrong choice does not produce an error anywhere: guests are simply checked against the wrong guest list. So every change shows what it affects and what it does
                not touch, and is applied only if nothing changed since the preview. Guests who are already online keep their access.
              </p>
            </HelpSection>
            <HelpSection title="What is not shown">
              <p>Archived networks keep their routing as history and are not listed here. A PMS that also keeps a route to an archived network is not a problem on its own.</p>
            </HelpSection>
          </>
        }
      />

      {roles !== null && !writable && policies && (
        <ReadOnlyNotice>Your role can see how room sign-in works on each network but not change it.</ReadOnlyNotice>
      )}

      {policiesFailed && policies && (
        <div className={FADE}>
          <Callout tone="warning">The networks could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}

      <RoomSignInReadinessCallout readiness={readiness} />

      {interfacesFailed ? (
        <div className={FADE}>
          <Callout tone="warning" title="The PMS connections could not be loaded">
            Choosing a PMS for a network is not possible until they load. Refresh to try again.
          </Callout>
        </div>
      ) : interfaces !== null && readyCount === 0 ? (
        <div className={FADE}>
          <Callout tone="warning" title={choosable.length === 0 ? "There is no PMS connection yet" : "No PMS connection is ready to be used"}>
            {choosable.length === 0
              ? "Room sign-in needs a PMS connection. Add one"
              : "A network can only be routed to a PMS connection that is in service and has a published configuration. Finish configuring one"}{" "}
            on <Link href="/pms-interfaces" className="underline underline-offset-2">PMS connection</Link> first.
          </Callout>
        </div>
      ) : null}

      <SectionCard flush title="Client networks" description="Archived networks are not listed. Choose Change to decide how room sign-in works on a network.">
        {loadFailed ? (
          <div role="alert" className={FADE}>
            <EmptyState
              size="sm"
              icon={<AlertTriangle />}
              title="The client networks could not be loaded"
              hint="Check the appliance is reachable, then try again. Nothing has been changed."
              action={
                <Button size="sm" variant="secondary" onClick={() => void refresh()} disabled={loading}>
                  <RefreshCw className={spin(loading)} /> Try again
                </Button>
              }
            />
          </div>
        ) : policies === null ? (
          <SkeletonRows rows={3} cols={4} />
        ) : policies.length === 0 ? (
          <EmptyState
            size="sm"
            icon={<Router />}
            title="No client network"
            hint="Room sign-in is chosen per client network. Create one first."
            action={<Link href="/network" className="text-sm text-primary underline-offset-4 hover:underline">Go to Client networks</Link>}
          />
        ) : (
          <Table label="Client networks and their room sign-in" aria-label="Client networks and their room sign-in">
            <THead>
              <TR>
                <TH>Client network</TH>
                <TH>Room sign-in</TH>
                <TH>Checks</TH>
                <TH>Status</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {policies.map((p) => {
                const a = stateOf.get(p.network_id);
                const st = a ? STATE_WORDS[a.state] : null;
                return (
                  <TR key={p.network_id}>
                    <TD>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="font-medium">{p.name || "Unnamed network"}</span>
                        {p.is_default && <Badge tone="info" size="sm">Default</Badge>}
                        {!p.enabled && <Badge tone="neutral" size="sm">Off</Badge>}
                      </div>
                    </TD>
                    <TD nowrap>{POLICY_WORDS[p.mode] ?? "Not recognised"}</TD>
                    <TD>
                      {p.interfaces.length === 0
                        ? <span className="text-muted-foreground">{p.mode === "NONE" ? "Nothing" : "No PMS chosen"}</span>
                        : <span>{p.interfaces.map((i) => i.label || "Unnamed PMS connection").join(", ")}</span>}
                    </TD>
                    <TD>
                      {a ? (
                        <>
                          <Badge tone={st?.tone ?? "neutral"} size="sm" dot>{st?.label ?? "Not recognised"}</Badge>
                          <div className="mt-0.5 max-w-xs text-xs text-muted-foreground">{a.summary}</div>
                        </>
                      ) : (
                        <span className="text-muted-foreground">{assessment ? "Not checked" : "Not known"}</span>
                      )}
                    </TD>
                    <TD className="w-0">
                      {writable && (
                        <RowActions
                          subject={p.name || "Unnamed network"}
                          primary={{ label: p.mode === "" ? "Decide" : "Change", onSelect: () => setEditing({ id: p.network_id, name: p.name || "Unnamed network" }) }}
                          items={p.mode === "" ? [] : [{ label: "Back to not decided", danger: true, onSelect: () => setClearing(p) }]}
                        />
                      )}
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      </SectionCard>

      <RoutingEditor
        open={editing !== null}
        network={editing}
        interfaces={choosable}
        onClose={() => setEditing(null)}
        onDone={refresh}
      />

      <ClearRoutingDialog
        open={clearing !== null}
        network={clearing ? { id: clearing.network_id, name: clearing.name || "this network" } : null}
        onClose={() => setClearing(null)}
        onDone={refresh}
      />
    </PageShell>
  );
}
