"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { RotateCcw } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SectionCard } from "@/components/ui/card";
import { DialogForm } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { Field, Input } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { useToast } from "@/components/ui/toast";
import { useNetworkAccess } from "@/components/network/shared";
import {
  ACTION_LABEL, LAYER_LABEL, OP_TITLE, STATE_LABEL, type RecoveryOp, type RecoveryPlan, type RecoveryStatus, type ReviewItem,
} from "@/lib/api/network-recovery";
import { errMsg } from "@/lib/utils";
import { GROUP_LABEL, type StagedGroup } from "@/lib/api/network-lifecycle";

const OPS: { op: RecoveryOp; text: string }[] = [
  { op: "restore_last_working", text: "Put every client network back to the configuration the appliance last confirmed working. Edits made since, and networks created since, are not kept live." },
  { op: "factory_defaults", text: "Return to the prepared network and switch every other client network off. Nothing is deleted." },
  { op: "full_reset", text: "As factory defaults, and also reset which portal design each network shows and each network's sign-in options. For a site you are starting again." },
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

  const load = useCallback(async () => {
    try { setStatus(await api.get<RecoveryStatus>("/network/recovery")); } catch { setStatus(null); }
    try { setItems((await api.get<{ data: ReviewItem[] }>("/network/recovery/review")).data ?? []); } catch { setItems([]); }
    try {
      const g = (await api.get<{ groups: StagedGroup[] }>("/network/changes")).groups ?? [];
      setStaged(g.filter((x) => ["RESTORE", "FACTORY_RESET", "FULL_RESET"].includes(x.group_kind)));
    } catch { setStaged([]); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function open(next: RecoveryOp) {
    setOp(next); setPlan(null); setTyped(""); setErr(null); setBusy(true);
    try { setPlan(await api.post<RecoveryPlan>("/network/recovery/preview", { op: next })); }
    catch (e) { setErr(e); }
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
    try { await api.post(`/network/recovery/review/${id}/acknowledge`, {}); await load(); }
    catch (e) { toast.error("Could not acknowledge", errMsg(e)); }
  }

  const needsName = plan?.confirm_site_name != null;
  const blocked = !plan || !plan.stageable || (needsName && typed.trim() !== plan.confirm_site_name);
  const why = !plan ? "Working out what would change…"
    : plan.conflicts.length ? "Resolve the conflict first."
    : plan.cannot_restore.length ? "Part of this cannot be restored, so none of it is staged."
    : plan.networks.length === 0 ? "Nothing would change."
    : "Type the confirmation to continue.";

  return (
    <PageShell width="wide">
      <PageHeader title="Recovery" description="Return the client networks to a known state. Each is staged, applied and kept or rolled back like any other network change." />

      {status?.latest && <RecoveryProgress latest={status.latest} />}

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
                  <strong>{i.network}</strong> — {i.detail}{" "}
                  {i.room_sign_in_paused && <Badge tone="warn">Room sign-in paused here</Badge>}
                  {i.layer === "pms_routing" && <> <Link className="underline" href="/pms-routing">PMS routing</Link></>}
                </span>
                {writable && <Button variant="secondary" size="sm" onClick={() => void acknowledge(i.id)}>Acknowledge</Button>}
              </li>
            ))}
          </ul>
        </SectionCard>
      )}

      {OPS.map(({ op: o, text }) => (
        <SectionCard key={o} title={OP_TITLE[o]} description={text}
          actions={writable ? <Button variant="secondary" size="sm" onClick={() => void open(o)}><RotateCcw /> Review…</Button> : undefined}>
          {o === "restore_last_working" && status && (
            <p className="text-sm text-muted-foreground">
              {status.last_working.available
                ? `Last confirmed working: revision ${status.last_working.revision}${status.last_working.confirmed_at ? `, ${new Date(status.last_working.confirmed_at).toLocaleString()}` : ""}.`
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
        {plan && (
          <div className="space-y-4 text-sm">
            {plan.conflicts.length > 0 && <Callout tone="warning" title="Cannot be staged"><ul className="list-disc pl-5">{plan.conflicts.map((c) => <li key={c}>{c}</li>)}</ul></Callout>}
            {plan.cannot_restore.length > 0 && <Callout tone="warning" title="Cannot be restored from here"><ul className="list-disc pl-5">{plan.cannot_restore.map((c) => <li key={c}>{c}</li>)}</ul></Callout>}
            <div>
              <strong>Client networks</strong>
              {plan.networks.length === 0 ? <p className="text-muted-foreground">Nothing would change.</p> : (
                <ul className="mt-1 space-y-1">
                  {plan.networks.map((n) => (
                    <li key={n.id || n.name}>
                      {n.name}: {ACTION_LABEL[n.action]}{n.changes?.length ? ` (${n.changes.join(", ")})` : ""}
                      {n.action === "switch_off" && n.active_clients > 0 && <span className="text-warning"> — {n.active_clients} connected device(s) lose access when it is applied</span>}
                    </li>
                  ))}
                </ul>
              )}
            </div>
            {plan.review.length > 0 && (
              <div>
                <strong>Portal design, sign-in and PMS routing</strong>
                <ul className="mt-1 space-y-1">
                  {plan.review.map((r, i) => (
                    <li key={i}>
                      {r.network ? `${r.network} — ` : ""}{LAYER_LABEL[r.layer] ?? r.layer}:{" "}
                      <Badge tone={r.state === "UNCHANGED" ? "neutral" : "warn"}>{STATE_LABEL[r.state] ?? r.state}</Badge>{" "}
                      <span className="text-muted-foreground">{r.detail}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <Callout tone="info" title="Check outside the appliance"><ul className="list-disc pl-5">{plan.switch_checklist.map((c) => <li key={c}>{c}</li>)}</ul></Callout>
            <div>
              <strong>Not touched</strong>
              <ul className="mt-1 list-disc pl-5 text-muted-foreground">{plan.protected.map((p) => <li key={p}>{p}</li>)}</ul>
            </div>
            {needsName && (
              <Field label={`Type ${plan.confirm_site_name} to confirm a full reset`}>
                <Input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
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
    <SectionCard title={`Latest recovery: ${title}`} description={new Date(latest.at).toLocaleString()}>
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
