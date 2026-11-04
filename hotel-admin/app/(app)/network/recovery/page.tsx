"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft, RefreshCw, RotateCcw } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button, LinkButton } from "@/components/ui/button";
import { SectionCard } from "@/components/ui/card";
import { DialogForm } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { LiveStatus } from "@/components/ui/patterns";
import { usePoll } from "@/lib/use-poll";
import { Field, Input } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { useToast } from "@/components/ui/toast";
import { useNetworkAccess } from "@/components/network/shared";
import {
  ACTION_LABEL, LAYER_LABEL, OP_TITLE, STATE_LABEL, type RecoveryOp, type RecoveryPlan, type RecoveryStatus, type ReviewItem,
} from "@/lib/api/network-recovery";
import { errMsg, formatDate } from "@/lib/utils";
import { RoutingEditor, usePmsRouting } from "@/components/pms/routing-editor";
import { POLICY_WORDS } from "@/lib/api/pms-routing";
import { GROUP_LABEL, type StagedGroup } from "@/lib/api/network-lifecycle";

const OPS: { op: RecoveryOp; text: string; destructive?: boolean }[] = [
  { op: "restore_last_working", text: "Put every client network back to the configuration the appliance last confirmed working. Edits made since, and networks created since, are not kept live." },
  { op: "factory_defaults", destructive: true, text: "Return to the prepared network and switch every other client network off. Nothing is deleted." },
  { op: "full_reset", destructive: true, text: "As factory defaults, and also reset which portal design each network shows and each network's sign-in options. For a site you are starting again." },
];

export default function RecoveryPage() {
  const toast = useToast();
  const router = useRouter();
  const { writable } = useNetworkAccess();
  const [staged, setStaged] = useState<StagedGroup[]>([]);
  const [status, setStatus] = useState<RecoveryStatus | null>(null);
  const [items, setItems] = useState<ReviewItem[]>([]);
  const [op, setOp] = useState<RecoveryOp | null>(null);
  const [plan, setPlan] = useState<RecoveryPlan | null>(null);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [previewFailed, setPreviewFailed] = useState(false);
  const [failed, setFailed] = useState<string[]>([]);
  const [refreshing, setRefreshing] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [acking, setAcking] = useState<string | null>(null);

  const load = useCallback(async () => {
    setRefreshing(true);
    const bad: string[] = [];
    try { setStatus(await api.get<RecoveryStatus>("/network/recovery")); } catch { bad.push("the recovery status"); }
    try { setItems((await api.get<{ data: ReviewItem[] }>("/network/recovery/review")).data ?? []); } catch { bad.push("the items that need review"); }
    try {
      const g = (await api.get<{ groups: StagedGroup[] }>("/network/changes")).groups ?? [];
      setStaged(g.filter((x) => ["RESTORE", "FACTORY_RESET", "FULL_RESET"].includes(x.group_kind)));
    } catch { bad.push("whether a recovery is already staged"); }
    setFailed(bad);
    if (bad.length < 3) setUpdatedAt(Date.now());
    setRefreshing(false);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const inFlight = staged.some((g) => g.state === "PENDING" || g.state === "APPLIED");
  const stagedUnknown = failed.includes("whether a recovery is already staged");
  usePoll(() => load(), 30000, { enabled: inFlight || status?.latest?.state === "STAGED" || status?.latest?.state === "APPLIED" });

  async function open(next: RecoveryOp) {
    setOp(next); setPlan(null); setTyped(""); setErr(null); setPreviewFailed(false); setBusy(true);
    try { setPlan(await api.post<RecoveryPlan>("/network/recovery/preview", { op: next })); }
    catch (e) { setErr(e); setPreviewFailed(true); }
    finally { setBusy(false); }
  }

  async function stage() {
    if (!op) return;
    setBusy(true); setErr(null);
    try {
      await api.post("/network/recovery/stage", { op, confirm_site_name: typed });
      toast.success(`${OP_TITLE[op]} is staged, not applied`, "Nothing has changed yet. Press Apply changes on Client networks, then keep the change before the confirmation window ends.");
      setOp(null);
      router.push("/network");
    } catch (e) { setErr(e); }
    finally { setBusy(false); }
  }

  async function acknowledge(id: string) {
    if (acking) return;
    setAcking(id);
    try { await api.post(`/network/recovery/review/${id}/acknowledge`, {}); await load(); }
    catch (e) { toast.error("Could not acknowledge", errMsg(e)); }
    finally { setAcking(null); }
  }

  const needsName = plan?.confirm_site_name != null;
  const nameMismatch = needsName && typed.trim() !== "" && typed.trim() !== plan?.confirm_site_name;
  const blocked = !plan || !plan.stageable || (needsName && typed.trim() !== plan.confirm_site_name);
  const why = !plan && previewFailed ? "The preview could not be worked out. Try again."
    : !plan ? "Working out what would change…"
    : plan.conflicts.length ? "Resolve the conflict first."
    : plan.cannot_restore.length ? "Part of this cannot be restored, so none of it is staged."
    : plan.networks.length === 0 ? "Nothing would change."
    : nameMismatch ? "The typed name does not match the site name."
    : "Type the confirmation to continue.";
  const reviewBlocked = inFlight
    ? "Keep or roll back the current recovery on Client networks first."
    : stagedUnknown ? "Whether a recovery is already staged could not be read. Refresh first." : null;

  return (
    <PageShell width="wide">
      <LinkButton href="/network" variant="ghost" size="sm" className="-ms-2.5 self-start">
        <ArrowLeft className="rtl:rotate-180" /> Client networks
      </LinkButton>
      <PageHeader
        title="Network recovery"
        description="Return the client networks to a known state. Each is staged, applied and kept or rolled back like any other network change."
        meta={updatedAt !== null && (
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} onRefresh={() => void load()} intervalSeconds={inFlight ? 30 : undefined} error={failed.length > 0} />
        )}
      />

      {failed.length > 0 && (
        <Callout tone="warning" title="Part of this page could not be loaded">
          Could not read {failed.join(", ")}. What is shown may be incomplete.{" "}
          <Button variant="link" size="sm" onClick={() => void load()}><RefreshCw /> Try again</Button>
        </Callout>
      )}

      {status?.latest && <RecoveryProgress latest={status.latest} />}
      {status?.latest?.room_sign_in_step && <RoomSignInStepCard step={status.latest.room_sign_in_step} onChanged={load} />}

      {staged.some((g) => g.state === "PENDING") && (
        <Callout tone="warning" title="A recovery is staged and has not been applied">
          {staged.filter((g) => g.state === "PENDING").map((g) => GROUP_LABEL[g.group_kind]).join(", ")}: the client networks are exactly as they were. It takes effect only when you press <strong>Apply
          changes</strong> on <Link className="underline" href="/network">Client networks</Link> and then keep the change.
        </Callout>
      )}
      {staged.some((g) => g.state === "APPLIED") && (
        <Callout tone="warning" title="A recovery is applied and waiting for you to keep it">
          {staged.filter((g) => g.state === "APPLIED").map((g) => GROUP_LABEL[g.group_kind]).join(", ")}: it is already affecting clients. Keep it or roll it back on{" "}
          <Link className="underline" href="/network">Client networks</Link> before the confirmation window ends, or the appliance puts the previous configuration back by itself.
        </Callout>
      )}

      {items.length > 0 && (
        <SectionCard title="Needs your review" description="Put back by a restore. Look at each and acknowledge it when you are happy.">
          <ul className="space-y-3">
            {items.map((i) => (
              <li key={i.id} className="flex flex-wrap items-center justify-between gap-3 text-sm">
                <span>
                  {i.network_id ? <Link className="font-semibold underline-offset-2 hover:underline" href={`/network/${i.network_id}`}>{i.network}</Link> : <strong>{i.network}</strong>} — {i.detail}{" "}
                  {i.room_sign_in_paused && <Badge tone="warn">Room sign-in paused here</Badge>}
                  {i.layer === "pms_routing" && <> <Link className="underline" href="/pms-routing">PMS routing</Link></>}
                </span>
                {writable && (
                  <Button variant="secondary" size="sm" disabled={acking !== null} onClick={() => void acknowledge(i.id)}>
                    {acking === i.id ? "Acknowledging…" : "Acknowledge"}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </SectionCard>
      )}

      {writable && reviewBlocked && <p className="text-caption text-muted-foreground">{reviewBlocked}</p>}
      {OPS.map(({ op: o, text, destructive }) => (
        <SectionCard key={o} title={OP_TITLE[o]} description={text}
          actions={
            <>
              {destructive && <Badge tone="warn">Switches networks off</Badge>}
              {writable && (
                <Button
                  variant={o === "full_reset" ? "danger" : "secondary"}
                  size="sm"
                  disabled={reviewBlocked !== null}
                  title={reviewBlocked ?? undefined}
                  onClick={() => void open(o)}
                >
                  <RotateCcw /> Review…
                </Button>
              )}
            </>
          }>
          {o === "restore_last_working" && status && (
            <p className="text-sm text-muted-foreground">
              {status.last_working.available
                ? `Last confirmed working: revision ${status.last_working.revision}${status.last_working.confirmed_at ? `, ${formatDate(status.last_working.confirmed_at)}` : ""}.`
                : "Nothing has been confirmed yet."}
            </p>
          )}
          {o !== "restore_last_working" && status && !status.factory.available && (
            <p className="text-sm text-muted-foreground">The prepared network ({status.factory.subnet}) has not been activated on this appliance yet.</p>
          )}
        </SectionCard>
      ))}

      <DialogForm open={op !== null} onOpenChange={(v) => !v && setOp(null)} size="lg" title={op ? OP_TITLE[op] : ""}
        description={plan?.summary ?? "Working out what would change…"} submitLabel="Stage this recovery" busy={busy} error={err}
        disabled={blocked} disabledReason={why} onSubmit={stage}>
        {!plan && previewFailed && op && (
          <Button type="button" variant="secondary" size="sm" onClick={() => void open(op)}><RefreshCw /> Try again</Button>
        )}
        {plan && (
          <div className="space-y-4 text-sm">
            {plan.conflicts.length > 0 && <Callout tone="warning" title="Cannot be staged"><ul className="list-disc ps-5">{plan.conflicts.map((c) => <li key={c}>{c}</li>)}</ul></Callout>}
            {plan.cannot_restore.length > 0 && <Callout tone="warning" title="Cannot be restored from here"><ul className="list-disc ps-5">{plan.cannot_restore.map((c) => <li key={c}>{c}</li>)}</ul></Callout>}
            <div>
              <strong>Client networks</strong>
              {plan.networks.length === 0 ? <p className="text-muted-foreground">Nothing would change.</p> : (
                <ul className="mt-1 space-y-1">
                  {plan.networks.map((n) => (
                    <li key={n.id || n.name}>
                      {n.name}: {ACTION_LABEL[n.action]}{n.changes?.length ? ` (${n.changes.join(", ")})` : ""}
                      {n.action === "switch_off" && n.active_clients > 0 && <span className="text-warning-subtle-foreground"> — {n.active_clients} connected device(s) lose access when it is applied</span>}
                    </li>
                  ))}
                </ul>
              )}
            </div>
            {plan.review.length > 0 && (
              <div>
                <strong>Portal design, sign-in and PMS routing</strong>
                <ul className="mt-1 space-y-1">
                  {plan.review.map((r) => (
                    <li key={`${r.network_id}|${r.layer}`}>
                      {r.network ? `${r.network} — ` : ""}{LAYER_LABEL[r.layer] ?? r.layer.replace(/_/g, " ")}:{" "}
                      <Badge tone={r.state === "UNCHANGED" ? "neutral" : "warn"}>{STATE_LABEL[r.state] ?? "Other"}</Badge>{" "}
                      <span className="text-muted-foreground">{r.detail}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <Callout tone="info" title="Check outside the appliance"><ul className="list-disc ps-5">{plan.switch_checklist.map((c) => <li key={c}>{c}</li>)}</ul></Callout>
            <div>
              <strong>Not touched</strong>
              <ul className="mt-1 list-disc ps-5 text-muted-foreground">{plan.protected.map((p) => <li key={p}>{p}</li>)}</ul>
            </div>
            {needsName && (
              <Field
                label={`Type ${plan.confirm_site_name} to confirm a full reset`}
                hint={<>Type <span className="font-mono">{plan.confirm_site_name}</span> exactly as shown.</>}
                error={nameMismatch ? "This does not match the site name." : undefined}
              >
                <Input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
              </Field>
            )}
          </div>
        )}
      </DialogForm>
    </PageShell>
  );
}

const STEPS = [
  { key: "STAGED", label: "Staged", text: "Nothing has changed yet. Press Apply changes on Client networks to start it." },
  { key: "APPLIED", label: "Applied", text: "It is live and affecting clients. Keep it or roll it back before the confirmation window ends; otherwise the appliance puts the previous configuration back by itself." },
  { key: "CONFIRMED", label: "Confirmed", text: "It was kept." },
] as const;

/** Where the latest recovery stands: staged, applied or confirmed are three different things and are never shown as one. */
function RecoveryProgress({ latest }: { latest: NonNullable<RecoveryStatus["latest"]> }) {
  const title = latest.kind === "FULL_RESET" ? OP_TITLE.full_reset : latest.kind === "FACTORY_RESET" ? OP_TITLE.factory_defaults : OP_TITLE.restore_last_working;
  if (latest.state === "CANCELLED") {
    return <SectionCard title={`Latest recovery: ${title}`}><p className="text-sm text-muted-foreground">Cancelled before it was applied. Nothing was changed.</p></SectionCard>;
  }
  const at = STEPS.findIndex((x) => x.key === latest.state);
  const retired = latest.state === "CONFIRMED" && latest.kind !== "RESTORE";
  return (
    <SectionCard title={`Latest recovery: ${title}`} description={formatDate(latest.at)}>
      <ol className="flex flex-wrap gap-2" aria-label="Progress">
        {STEPS.map((x, i) => (
          <li key={x.key} aria-current={i === at ? "step" : undefined}>
            <Badge tone={i < at ? "ok" : i === at ? (latest.state === "CONFIRMED" ? "ok" : "warn") : "default"}>{i + 1}. {x.label}</Badge>
          </li>
        ))}
      </ol>
      <p className="mt-2 text-sm">{STEPS[at].text}{retired ? " The networks it switched off are archived and the Default Network serves clients." : ""}</p>
    </SectionCard>
  );
}

/**
 * After a CONFIRMED reset: how room sign-in works on the resulting network. Optional, and separate from the reset: the reset never touched PMS routing, and nothing
 * changes until the operator has seen the preview and confirmed it.
 */
function RoomSignInStepCard({ step, onChanged }: { step: NonNullable<NonNullable<RecoveryStatus["latest"]>["room_sign_in_step"]>; onChanged: () => void | Promise<void> }) {
  const [open, setOpen] = useState(false);
  const { canRoute, interfaces } = usePmsRouting();
  return (
    <SectionCard title="Room sign-in on the new network (optional)" description={`${step.network_name ?? "The Default Network"} · ${POLICY_WORDS[step.policy] ?? "Not decided"}`}>
      {step.offered ? (
        <>
          <p className="text-sm">
            The reset did not change any PMS routing. Room sign-in is not offered on {step.network_name ?? "the Default Network"} until you choose how it works: none, one dedicated PMS, or a shared
            strict set of two or three. You see what it affects before anything is applied. You can skip this and decide later under <Link className="underline" href="/pms-routing">PMS routing</Link>.
          </p>
          {canRoute && <Button className="mt-3" size="sm" onClick={() => setOpen(true)}>Configure room sign-in…</Button>}
        </>
      ) : (
        <p className="text-sm text-muted-foreground">{step.summary} <Link className="underline" href="/pms-routing">PMS routing</Link></p>
      )}
      <RoutingEditor open={open} network={step.network_id ? { id: step.network_id, name: step.network_name ?? "the Default Network" } : null} interfaces={interfaces}
        onClose={() => setOpen(false)} onDone={onChanged} />
    </SectionCard>
  );
}
