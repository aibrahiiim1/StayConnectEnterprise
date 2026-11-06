"use client";

// Enforcement policy: what a signed-in client may reach and how fast an used-up allowance cuts it off. One audited
// singleton on the appliance; every change needs a reason and is kept in the history below. The server holds the
// bounds and sends them with the values.

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { errMsg, formatDate } from "@/lib/utils";
import {
  EnforcementChange, EnforcementDoc, EnforcementSetting, FIELD_LABEL, TURN_OFF_PHRASE,
  allowListProblem, cidrProblem, continuityProblem, continuityWords, sameList, splitCidrs, valueWords,
} from "@/lib/enforcement-policy";
import { REASON_HINT, REASON_MAX_CHARS, reasonOk } from "@/lib/reason";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input, Textarea } from "@/components/ui/input";
import { Skeleton, Switch } from "@/components/ui/misc";
import { EmptyState } from "@/components/ui/empty-state";
import { AlertTriangle, RefreshCw } from "lucide-react";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { HelpSection } from "@/components/help";
import { useOperatorRoles } from "@/lib/whoami-context";

const HELP = (
  <>
    <HelpSection title="What this controls">
      <p>
        These settings decide what a signed-in client can reach on the hotel&apos;s own networks, and how quickly a
        client whose allowance is used up is cut off. The shipped values are safe; change them only for a reason you
        can write down, because every change is recorded with your name.
      </p>
    </HelpSection>
    <HelpSection title="Taking effect">
      <p>
        Changes are picked up by the appliance by itself within moments; nothing needs restarting. Clients already
        online keep their sessions.
      </p>
    </HelpSection>
  </>
);

function setting(doc: EnforcementDoc, field: string): EnforcementSetting {
  return doc.settings.find((s) => s.field === field) ?? { field, label: field, default: "", explanation: "" };
}

function defaultWords(s: EnforcementSetting): string {
  const d = s.default;
  if (typeof d === "boolean") return d ? "on" : "off";
  if (Array.isArray(d)) return d.length === 0 ? "none" : d.join(", ");
  return `${d}${s.unit ? ` ${s.unit}` : ""}`;
}

const DESCRIPTION = "What clients may reach, and how fast a used-up allowance cuts them off.";
const HISTORY_PREVIEW = 8;

function secondsWords(n: number): string {
  if (!Number.isInteger(n) || n <= 0) return "";
  const m = Math.floor(n / 60), r = n % 60;
  const part = (v: number, unit: string) => (v ? `${v} ${unit}${v === 1 ? "" : "s"}` : "");
  return [part(m, "minute"), part(r, "second")].filter(Boolean).join(" ");
}

// The browser's Back/Forward: neither a click nor beforeunload, so a guard entry is kept on top of history while there are changes.
const HISTORY_BACK = "history:back";

function useLeaveGuard(dirty: boolean) {
  const router = useRouter();
  const [target, setTarget] = useState<string | null>(null);
  const leaving = useRef(false);
  // The guard entry exists only while there are unsaved changes: pushed when they appear, stepped off when the draft is clean again
  // (saved or reverted), so no stale entry outlives the draft for Back or Forward to land on.
  // A guard entry the page finds itself on (reached by Forward, by Back from another screen, or a reload) is reused, never doubled.
  const isGuard = (st: unknown) => !!(st as { leaveGuard?: boolean } | null)?.leaveGuard;
  const onGuard = useRef(typeof window !== "undefined" && isGuard(window.history.state));
  const stepping = useRef(false);
  const dirtyNow = useRef(dirty);
  dirtyNow.current = dirty;
  const pushGuard = () => window.history.pushState({ ...window.history.state, leaveGuard: true }, "", window.location.href);
  useEffect(() => {
    if (dirty && !onGuard.current) {
      pushGuard();
      onGuard.current = true;
    } else if (!dirty && onGuard.current) {
      onGuard.current = false;
      stepping.current = true;
      window.history.back();
    }
  }, [dirty]);
  useEffect(() => {
    const here = window.location.pathname + window.location.search;
    const onPop = (e: PopStateEvent) => {
      if (stepping.current) { stepping.current = false; return; }
      if (leaving.current || window.location.pathname + window.location.search !== here) return;
      if (isGuard(e.state)) { onGuard.current = true; return; }
      if (!onGuard.current) return;
      // Back off the guard onto this page's own entry: with changes the guard is restored and the operator asked; without, it is simply left.
      if (!dirtyNow.current) { onGuard.current = false; return; }
      pushGuard();
      setTarget(HISTORY_BACK);
    };
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    const intercept = (e: MouseEvent) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const a = (e.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
      if (!a || a.target === "_blank" || a.origin !== window.location.origin) return;
      if (a.pathname === window.location.pathname && a.search === window.location.search) return;
      e.preventDefault();
      e.stopPropagation();
      setTarget(a.pathname + a.search + a.hash);
    };
    window.addEventListener("beforeunload", warn);
    document.addEventListener("click", intercept, true);
    return () => {
      window.removeEventListener("beforeunload", warn);
      document.removeEventListener("click", intercept, true);
    };
  }, [dirty]);
  return (
    <ConfirmDialog
      open={target !== null}
      onOpenChange={(v) => !v && setTarget(null)}
      title="Leave without saving?"
      description="The protection settings you changed here have not been saved."
      confirmLabel="Discard and leave"
      confirmVariant="danger"
      onConfirm={() => {
        const t = target;
        setTarget(null);
        if (t === HISTORY_BACK) {
          // Past the guard entry and the page itself, to where Back was going.
          leaving.current = true;
          window.history.go(-2);
        } else if (t) {
          // From the guard entry, the destination takes its place, so Back from there returns to this page once, not twice.
          leaving.current = true;
          if (onGuard.current) { onGuard.current = false; router.replace(t); } else router.push(t);
        }
      }}
    />
  );
}

type Draft = { deny: boolean; upstream: boolean; mac: boolean; cutoff: string; cidrs: string; continuity: string };

function draftOf(doc: EnforcementDoc): Draft {
  const p = doc.policy;
  return {
    deny: p.private_deny_enabled, upstream: p.allow_upstream_infrastructure, mac: p.mac_binding_enabled,
    cutoff: String(p.cutoff_seconds), cidrs: p.private_allow_cidrs.join("\n"),
    continuity: p.outage_continuity_minutes === undefined ? "" : String(p.outage_continuity_minutes),
  };
}

export default function EnforcementPolicyPage() {
  const roles = useOperatorRoles();
  const writable = roles !== null && canWrite("network", roles);
  const [doc, setDoc] = useState<EnforcementDoc | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [saveErr, setSaveErr] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [confirmOff, setConfirmOff] = useState(false);
  const [saved, setSaved] = useState(false);
  const [allHistory, setAllHistory] = useState(false);
  const [guardOn, setGuardOn] = useState(false);
  const leaveDialog = useLeaveGuard(guardOn);

  const load = useCallback(async () => {
    try {
      const d = await api.get<EnforcementDoc>("/network/enforcement-policy");
      setDoc(d);
      setDraft(draftOf(d));
      setErr(null);
    } catch (e) {
      setErr(e);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    setGuardOn(!!doc && !!draft && (JSON.stringify(draft) !== JSON.stringify(draftOf(doc)) || reason !== "" || password !== ""));
  }, [doc, draft, reason, password]);

  if (!doc || !draft) {
    return (
      <PageShell>
        <PageHeader title="Client access protection" description={DESCRIPTION} help={HELP} />
        {err ? (
          <EmptyState
            icon={<AlertTriangle />}
            title="The protection settings could not be loaded"
            hint={errMsg(err)}
            action={<Button variant="secondary" onClick={() => void load()}><RefreshCw /> Try again</Button>}
          />
        ) : <Skeleton className="h-96 w-full" />}
      </PageShell>
    );
  }

  const lim = doc.limits;
  const cur = doc.policy;
  const entries = splitCidrs(draft.cidrs);
  const listProblem = allowListProblem(entries, lim.max_cidrs, lim.min_prefix, lim.max_prefix);
  const cutoffN = Number(draft.cutoff);
  const cutoffProblem =
    draft.cutoff.trim() === "" || !Number.isInteger(cutoffN) || cutoffN < lim.cutoff_min || cutoffN > lim.cutoff_max
      ? `Enter a whole number of seconds between ${lim.cutoff_min} and ${lim.cutoff_max}.`
      : null;

  // Older appliances do not serve the continuity setting; the row is then not shown and nothing about it is sent.
  const hasContinuity = cur.outage_continuity_minutes !== undefined;
  const contN = Number(draft.continuity);
  const contProblem = hasContinuity ? continuityProblem(draft.continuity, lim.continuity_min ?? 0, lim.continuity_max ?? 120) : null;
  const contDirty = hasContinuity && contProblem === null && contN !== cur.outage_continuity_minutes;

  const changed = {
    private_deny_enabled: draft.deny !== cur.private_deny_enabled,
    private_allow_cidrs: !sameList(entries, cur.private_allow_cidrs),
    allow_upstream_infrastructure: draft.upstream !== cur.allow_upstream_infrastructure,
    mac_binding_enabled: draft.mac !== cur.mac_binding_enabled,
    cutoff_seconds: draft.cutoff.trim() !== String(cur.cutoff_seconds),
    outage_continuity_minutes: hasContinuity && draft.continuity.trim() !== String(cur.outage_continuity_minutes),
  };
  const dirty =
    contDirty || changed.private_deny_enabled || changed.allow_upstream_infrastructure ||
    changed.mac_binding_enabled || cutoffN !== cur.cutoff_seconds || changed.private_allow_cidrs;
  const editing = dirty || changed.cutoff_seconds || changed.outage_continuity_minutes || reason !== "" || password !== "";
  const validEntries = entries.filter((e) => !cidrProblem(e, lim.min_prefix, lim.max_prefix));
  const invalidEntries = entries.length - validEntries.length;
  const summary: string[] = [];
  const line = (f: string, from: string, to: string) => summary.push(`${FIELD_LABEL[f] ?? setting(doc, f).label}: ${valueWords(f, from)} → ${valueWords(f, to)}`);
  if (changed.private_deny_enabled) line("private_deny_enabled", String(cur.private_deny_enabled), String(draft.deny));
  if (changed.private_allow_cidrs) line("private_allow_cidrs", cur.private_allow_cidrs.join(","), entries.join(","));
  if (changed.allow_upstream_infrastructure) line("allow_upstream_infrastructure", String(cur.allow_upstream_infrastructure), String(draft.upstream));
  if (changed.mac_binding_enabled) line("mac_binding_enabled", String(cur.mac_binding_enabled), String(draft.mac));
  if (cutoffN !== cur.cutoff_seconds && !cutoffProblem) line("cutoff_seconds", String(cur.cutoff_seconds), String(cutoffN));
  if (contDirty) line("outage_continuity_minutes", String(cur.outage_continuity_minutes), String(contN));
  const turningDenialOff = cur.private_deny_enabled && !draft.deny;
  const reasonProblem = dirty && !reasonOk(reason) ? "A reason is required: " + REASON_HINT : null;
  const blocked = !dirty || !!listProblem || !!cutoffProblem || !!contProblem || !!reasonProblem || password === "" || saving;
  const whyBlocked = saving ? null
    : !dirty ? "Change a setting to save."
    : listProblem ? "Fix the allow-list first."
    : cutoffProblem ? "Fix the cut-off delay first."
    : contProblem ? "Fix the outage setting first."
    : !reasonOk(reason) && password === "" ? "Enter a reason and your password to save."
    : !reasonOk(reason) ? "Enter a reason to save."
    : password === "" ? "Enter your password to save."
    : null;

  async function save() {
    if (!draft) return;
    setSaving(true);
    setSaveErr(null);
    setSaved(false);
    try {
      // Only what changed is sent; the server also ignores unchanged fields.
      const body: Record<string, unknown> = { reason: reason.trim(), password };
      if (draft.deny !== cur.private_deny_enabled) body.private_deny_enabled = draft.deny;
      if (draft.upstream !== cur.allow_upstream_infrastructure) body.allow_upstream_infrastructure = draft.upstream;
      if (draft.mac !== cur.mac_binding_enabled) body.mac_binding_enabled = draft.mac;
      if (cutoffN !== cur.cutoff_seconds) body.cutoff_seconds = cutoffN;
      if (!sameList(entries, cur.private_allow_cidrs)) body.private_allow_cidrs = entries;
      if (contDirty) body.outage_continuity_minutes = contN;
      const next = await api.patch<EnforcementDoc>("/network/enforcement-policy", body);
      setDoc(next);
      setDraft(draftOf(next));
      setReason("");
      setPassword("");
      setConfirmOff(false);
      setSaved(true);

    } catch (e) {
      setSaveErr(e);
      if (e instanceof ApiError && e.status === 401) setErr(e);
    } finally {
      setSaving(false);
    }
  }

  const ro = !writable || saving;
  const row = "flex flex-wrap items-start justify-between gap-x-6 gap-y-2 border-b border-border py-4 first:pt-0 last:border-b-0 last:pb-0";
  const notSaved = (f: keyof typeof changed) => (changed[f] ? <Badge tone="info" className="ms-2 align-middle">Not saved</Badge> : null);
  const desc = (f: string) => {
    const s = setting(doc, f);
    return (
      <p className="mt-1 max-w-prose text-caption text-muted-foreground">
        {s.explanation} <span className="whitespace-nowrap">Default: {defaultWords(s)}.</span>
      </p>
    );
  };

  return (
    <PageShell width="default">
      <PageHeader title="Client access protection" description={DESCRIPTION} help={HELP} />
      {!writable && <ReadOnlyNotice>Your role can see these settings but not change them.</ReadOnlyNotice>}
      {err ? <ErrorBanner err={err} /> : null}

      <SectionCard
        title="Settings"
        description={`Version ${cur.config_version}${cur.updated_by ? `, last changed by ${cur.updated_by}` : ""}.`}
      >
        <div>
          <div className={row}>
            <div className="min-w-0 flex-1">
              <label htmlFor="enf-deny" className="text-label">{setting(doc, "private_deny_enabled").label}</label>{notSaved("private_deny_enabled")}
              {desc("private_deny_enabled")}
            </div>
            <div className="flex items-center gap-2">
              <Switch id="enf-deny" checked={draft.deny} disabled={ro} onCheckedChange={(v) => setDraft({ ...draft, deny: v })}
                label={setting(doc, "private_deny_enabled").label} />
              <Badge tone={draft.deny ? "ok" : "warn"}>{draft.deny ? "On" : "Off"}</Badge>
            </div>
          </div>

          <div className={row}>
            <div className="min-w-0 flex-1">
              <label htmlFor="enf-cidrs" className="text-label">{setting(doc, "private_allow_cidrs").label}</label>{notSaved("private_allow_cidrs")}
              {desc("private_allow_cidrs")}
            </div>
            <div className="w-full sm:w-80">
              <Field
                hint={`One network per line, IPv4 only, /${lim.min_prefix} to /${lim.max_prefix}, at most ${lim.max_cidrs}. ${validEntries.length} valid${invalidEntries ? `, ${invalidEntries} to fix` : ""}.`}
                error={writable ? listProblem : null}
              >
                <Textarea id="enf-cidrs" rows={4} value={draft.cidrs} readOnly={ro} disabled={ro} spellCheck={false}
                  className="font-mono" placeholder="192.168.50.0/24" onChange={(e) => setDraft({ ...draft, cidrs: e.target.value })} />
              </Field>
            </div>
          </div>

          <div className={row}>
            <div className="min-w-0 flex-1">
              <label htmlFor="enf-upstream" className="text-label">{setting(doc, "allow_upstream_infrastructure").label}</label>{notSaved("allow_upstream_infrastructure")}
              {desc("allow_upstream_infrastructure")}
            </div>
            <div className="flex items-center gap-2">
              <Switch id="enf-upstream" checked={draft.upstream} disabled={ro} onCheckedChange={(v) => setDraft({ ...draft, upstream: v })}
                label={setting(doc, "allow_upstream_infrastructure").label} />
              <Badge tone={draft.upstream ? "ok" : "default"}>{draft.upstream ? "On" : "Off"}</Badge>
            </div>
          </div>

          <div className={row}>
            <div className="min-w-0 flex-1">
              <label htmlFor="enf-mac" className="text-label">{setting(doc, "mac_binding_enabled").label}</label>{notSaved("mac_binding_enabled")}
              {desc("mac_binding_enabled")}
            </div>
            <div className="flex items-center gap-2">
              <Switch id="enf-mac" checked={draft.mac} disabled={ro} onCheckedChange={(v) => setDraft({ ...draft, mac: v })}
                label={setting(doc, "mac_binding_enabled").label} />
              <Badge tone={draft.mac ? "ok" : "default"}>{draft.mac ? "On" : "Off"}</Badge>
            </div>
          </div>

          <div className={row}>
            <div className="min-w-0 flex-1">
              <label htmlFor="enf-cutoff" className="text-label">{setting(doc, "cutoff_seconds").label}</label>{notSaved("cutoff_seconds")}
              {desc("cutoff_seconds")}
            </div>
            <div className="w-full sm:w-80">
              <Field
                hint={`Allowed ${lim.cutoff_min} to ${lim.cutoff_max} seconds.${!cutoffProblem && cutoffN >= 60 ? ` That is ${secondsWords(cutoffN)}.` : ""}`}
                error={writable ? cutoffProblem : null}
              >
                <div className="flex items-center gap-2">
                  <Input id="enf-cutoff" inputMode="numeric" className="w-24" value={draft.cutoff} readOnly={ro} disabled={ro}
                    onChange={(e) => setDraft({ ...draft, cutoff: e.target.value })} />
                  <span className="text-sm text-muted-foreground">seconds</span>
                </div>
              </Field>
            </div>
          </div>

          {hasContinuity && (
            <div className={row}>
              <div className="min-w-0 flex-1">
                <label htmlFor="enf-continuity" className="text-label">{setting(doc, "outage_continuity_minutes").label}</label>{notSaved("outage_continuity_minutes")}
                {desc("outage_continuity_minutes")}
                <p className="mt-2 max-w-prose text-caption" data-testid="continuity-effect">
                  {contProblem ? "" : continuityWords(contN)}
                </p>
              </div>
              <div className="w-full sm:w-80">
                <Field hint={`Allowed ${lim.continuity_min ?? 0} to ${lim.continuity_max ?? 120} minutes. 0 turns it off.`} error={writable ? contProblem : null}>
                  <div className="flex items-center gap-2">
                    <Input id="enf-continuity" inputMode="numeric" className="w-24" value={draft.continuity} readOnly={ro} disabled={ro}
                      onChange={(ev) => setDraft({ ...draft, continuity: ev.target.value })} />
                    <span className="text-sm text-muted-foreground">minutes</span>
                  </div>
                </Field>
              </div>
            </div>
          )}
        </div>

        {writable && (
          <form
            className="mt-5 space-y-3 border-t border-border pt-5"
            onSubmit={(e) => {
              e.preventDefault();
              if (blocked) return;
              setSaveErr(null);
              if (turningDenialOff) setConfirmOff(true); else void save();
            }}
          >
            {turningDenialOff && (
              <Callout tone="warning" title="Private-network denial will be turned off">
                Clients will again be able to reach the hotel&apos;s private networks, except the management networks. You
                will be asked to confirm before this is saved.
              </Callout>
            )}
            <Field label="Reason for the change" required htmlFor="enf-reason" hint={dirty ? REASON_HINT : "Change a setting above to enter a reason."}
              error={dirty && reason !== "" ? reasonProblem : null}>
              <Input id="enf-reason" value={reason} maxLength={REASON_MAX_CHARS} disabled={!dirty || saving}
                onChange={(e) => setReason(e.target.value)} placeholder="For example: allow the front-desk printer network" />
            </Field>
            <Field label="Your password" required>
              <Input type="password" autoComplete="current-password" value={password} disabled={!dirty || saving}
                onChange={(e) => setPassword(e.target.value)} />
            </Field>
            {saveErr && !confirmOff ? <ErrorBanner err={saveErr} className="mb-0" /> : null}
            {summary.length > 0 && (
              <div className="rounded-md border border-border bg-surface px-3 py-2 text-sm">
                <div className="text-label">Changes to save</div>
                <ul className="mt-1 space-y-0.5 break-words text-caption text-muted-foreground">
                  {summary.map((l) => <li key={l}>{l}</li>)}
                </ul>
              </div>
            )}
            {saved && !editing && <p role="status" className="text-sm text-success">Saved. The appliance applies the new values within moments.</p>}
            <div className="flex flex-wrap items-center justify-end gap-2">
              {whyBlocked && <span className="me-auto text-caption text-muted-foreground">{whyBlocked}</span>}
              {editing && !saving && (
                <Button type="button" variant="ghost" onClick={() => { setDraft(draftOf(doc)); setReason(""); setPassword(""); setSaveErr(null); }}>Discard</Button>
              )}
              <Button type="submit" disabled={blocked}>
                {saving ? "Saving…" : "Save settings"}
              </Button>
            </div>
          </form>
        )}
      </SectionCard>

      <SectionCard title="Change history">
        <History items={allHistory ? doc.history : doc.history.slice(0, HISTORY_PREVIEW)} />
        {doc.history.length > HISTORY_PREVIEW && (
          <Button size="sm" variant="ghost" className="mt-2" onClick={() => setAllHistory((v) => !v)}>
            {allHistory ? "Show fewer" : `View all ${doc.history.length}`}
          </Button>
        )}
      </SectionCard>

      <ConfirmDialog
        open={confirmOff}
        onOpenChange={(v) => !v && !saving && setConfirmOff(false)}
        title="Turn off private-network denial?"
        description="Signed-in clients will be able to reach the hotel's private networks again."
        consequences={[
          "Anything on the hotel's private networks that is not protected by its own sign-in becomes reachable from client devices.",
          "The management networks stay protected.",
          `Reason recorded: ${reason.trim()}`,
        ]}
        confirmLabel="Turn off denial and save"
        confirmVariant="danger"
        confirmText={TURN_OFF_PHRASE}
        busy={saving}
        error={saveErr}
        onConfirm={() => save()}
      />
      {leaveDialog}
    </PageShell>
  );
}

function History({ items }: { items: EnforcementChange[] }) {
  if (items.length === 0) return <p className="text-sm text-muted-foreground">No changes recorded yet.</p>;
  return (
    <ol aria-label="Change history" className="divide-y divide-border">
      {items.map((c, i) => (
        <li key={`${c.config_version}-${c.field}-${i}`} className="py-2.5 text-sm">
          <div className="flex flex-wrap items-baseline justify-between gap-x-4">
            <span className="font-medium">{FIELD_LABEL[c.field] ?? c.field}</span>
            <span className="text-caption text-muted-foreground">{[formatDate(c.changed_at), c.changed_by].filter(Boolean).join(" · ")}</span>
          </div>
          {c.field !== "all" && (
            <div className="break-words text-caption text-muted-foreground">
              {valueWords(c.field, c.old_value)} → {valueWords(c.field, c.new_value)}
            </div>
          )}
          <div className="text-caption">{c.reason}</div>
        </li>
      ))}
    </ol>
  );
}

