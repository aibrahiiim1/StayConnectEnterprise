"use client";

// Enforcement policy: what a signed-in client may reach and how fast an used-up allowance cuts it off. One audited
// singleton on the appliance; every change needs a reason and is kept in the history below. The server holds the
// bounds and sends them with the values.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { formatDate } from "@/lib/utils";
import {
  EnforcementChange, EnforcementDoc, EnforcementSetting, FIELD_LABEL, TURN_OFF_PHRASE,
  allowListProblem, sameList, splitCidrs, valueWords,
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
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
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

type Draft = { deny: boolean; upstream: boolean; mac: boolean; cutoff: string; cidrs: string };

function draftOf(doc: EnforcementDoc): Draft {
  const p = doc.policy;
  return {
    deny: p.private_deny_enabled, upstream: p.allow_upstream_infrastructure, mac: p.mac_binding_enabled,
    cutoff: String(p.cutoff_seconds), cidrs: p.private_allow_cidrs.join("\n"),
  };
}

export default function EnforcementPolicyPage() {
  const toast = useToast();
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

  if (!doc || !draft) {
    return (
      <PageShell>
        <PageHeader title="Client access protection" description="What clients may reach, and how fast an used-up allowance cuts them off." help={HELP} />
        {err ? <ErrorBanner err={err} /> : <Skeleton className="h-96 w-full" />}
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

  const dirty =
    draft.deny !== cur.private_deny_enabled || draft.upstream !== cur.allow_upstream_infrastructure ||
    draft.mac !== cur.mac_binding_enabled || cutoffN !== cur.cutoff_seconds || !sameList(entries, cur.private_allow_cidrs);
  const turningDenialOff = cur.private_deny_enabled && !draft.deny;
  const reasonProblem = dirty && !reasonOk(reason) ? "A reason is required: " + REASON_HINT : null;
  const blocked = !dirty || !!listProblem || !!cutoffProblem || !!reasonProblem || password === "" || saving;

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
      const next = await api.patch<EnforcementDoc>("/network/enforcement-policy", body);
      setDoc(next);
      setDraft(draftOf(next));
      setReason("");
      setPassword("");
      setConfirmOff(false);
      setSaved(true);
      toast.success("Protection settings saved", "The appliance applies them within moments.");
    } catch (e) {
      setSaveErr(e);
      if (e instanceof ApiError && e.status === 401) setErr(e);
    } finally {
      setSaving(false);
    }
  }

  const ro = !writable || saving;
  const row = "flex flex-wrap items-start justify-between gap-x-6 gap-y-2 border-b border-border py-4 first:pt-0 last:border-b-0 last:pb-0";
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
      <PageHeader title="Client access protection" description="What clients may reach, and how fast an used-up allowance cuts them off." help={HELP} />
      {!writable && <ReadOnlyNotice>Your role can see these settings but not change them.</ReadOnlyNotice>}
      {err ? <ErrorBanner err={err} /> : null}
      {saved && !dirty && <Callout tone="success">Saved. The appliance applies the new values within moments.</Callout>}

      <SectionCard
        title="Settings"
        description={`Version ${cur.config_version}${cur.updated_by ? `, last changed by ${cur.updated_by}` : ""}.`}
      >
        <div>
          <div className={row}>
            <div className="min-w-0 flex-1">
              <label htmlFor="enf-deny" className="text-label">{setting(doc, "private_deny_enabled").label}</label>
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
              <label htmlFor="enf-cidrs" className="text-label">{setting(doc, "private_allow_cidrs").label}</label>
              {desc("private_allow_cidrs")}
            </div>
            <div className="w-full sm:w-80">
              <Field
                hint={`One network per line, IPv4 only, /${lim.min_prefix} to /${lim.max_prefix}, at most ${lim.max_cidrs}. Currently ${entries.length}.`}
                error={writable ? listProblem : null}
              >
                <Textarea id="enf-cidrs" rows={4} value={draft.cidrs} readOnly={ro} disabled={ro} spellCheck={false}
                  className="font-mono" placeholder="192.168.50.0/24" onChange={(e) => setDraft({ ...draft, cidrs: e.target.value })} />
              </Field>
            </div>
          </div>

          <div className={row}>
            <div className="min-w-0 flex-1">
              <label htmlFor="enf-upstream" className="text-label">{setting(doc, "allow_upstream_infrastructure").label}</label>
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
              <label htmlFor="enf-mac" className="text-label">{setting(doc, "mac_binding_enabled").label}</label>
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
              <label htmlFor="enf-cutoff" className="text-label">{setting(doc, "cutoff_seconds").label}</label>
              {desc("cutoff_seconds")}
            </div>
            <div className="w-full sm:w-80">
              <Field hint={`Allowed ${lim.cutoff_min} to ${lim.cutoff_max} seconds.`} error={writable ? cutoffProblem : null}>
                <div className="flex items-center gap-2">
                  <Input id="enf-cutoff" inputMode="numeric" className="w-24" value={draft.cutoff} readOnly={ro} disabled={ro}
                    onChange={(e) => setDraft({ ...draft, cutoff: e.target.value })} />
                  <span className="text-sm text-muted-foreground">seconds</span>
                </div>
              </Field>
            </div>
          </div>
        </div>

        {writable && (
          <div className="mt-5 space-y-3 border-t border-border pt-5">
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
            <Field label="Your password" required hint="Confirms the change.">
              <Input type="password" autoComplete="current-password" value={password} disabled={!dirty || saving}
                onChange={(e) => setPassword(e.target.value)} />
            </Field>
            {saveErr && !confirmOff ? <ErrorBanner err={saveErr} className="mb-0" /> : null}
            <div className="flex justify-end gap-2">
              {dirty && !saving && <Button variant="ghost" onClick={() => { setDraft(draftOf(doc)); setReason(""); setSaveErr(null); }}>Discard</Button>}
              <Button disabled={blocked} onClick={() => { setSaveErr(null); if (turningDenialOff) setConfirmOff(true); else void save(); }}>
                {saving ? "Saving…" : "Save settings"}
              </Button>
            </div>
          </div>
        )}
      </SectionCard>

      <SectionCard title="Change history" description="Every change to these settings, newest first. Entries cannot be edited or removed.">
        <History items={doc.history} />
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
            <span className="text-caption text-muted-foreground">{formatDate(c.changed_at)} · {c.changed_by}</span>
          </div>
          {c.field !== "all" && (
            <div className="text-caption text-muted-foreground">
              {valueWords(c.field, c.old_value)} → {valueWords(c.field, c.new_value)}
            </div>
          )}
          <div className="text-caption">{c.reason}</div>
        </li>
      ))}
    </ol>
  );
}

