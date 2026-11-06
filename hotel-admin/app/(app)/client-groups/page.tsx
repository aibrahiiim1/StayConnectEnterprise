"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ApiError, ListResp, ClientGroup, ClientGroupChange, ClientGroupRule, ClientGroupRuleType } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card } from "@/components/ui/card";
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { ChangeDiff, HistoryList } from "@/components/ui/history";
import { RowActions } from "@/components/ui/row-actions";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select, Textarea, QuietHints } from "@/components/ui/input";
import { InfoHint } from "@/components/ui/quiet";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { Switch, SkeletonRows } from "@/components/ui/misc";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Plus, Trash2, UsersRound, ArrowUpRight, AlertTriangle, RefreshCw, Pencil } from "lucide-react";
import { canRead, canWrite } from "@/lib/roles";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { cn, formatDate } from "@/lib/utils";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";
import { ruleSummary } from "./rule-summary";
import { useOperatorRoles } from "@/lib/whoami-context";

const PRIORITY_DEFAULT = 100;
const PRIORITY_MIN = 1;
const PRIORITY_MAX = 1000;
const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const RULE_KINDS: { type: ClientGroupRuleType; label: string; assurance: string; hint: string }[] = [
  {
    type: "EMAIL_DOMAIN",
    label: "Verified email address domain",
    assurance: "Verified email address",
    hint: "Suitable for commercial benefits such as partner pricing. Anyone who can receive mail at the domain qualifies. The address must be proven: a verified Email account (the client confirmed it from the verification email), or a Google, Apple or Microsoft sign-in that asserts it. An unverified Email account, and an account staff created, are not placed in the group.",
  },
  {
    type: "IDP_TENANT",
    label: "Microsoft organisation (Entra tenant)",
    assurance: "Organisation account",
    hint: "Strong: the client signed in with a Microsoft account in your organisation's directory. Requires the Microsoft identity provider.",
  },
  {
    type: "IDP_HOSTED_DOMAIN",
    label: "Google Workspace organisation",
    assurance: "Organisation account",
    hint: "Strong: the client signed in with a Google Workspace account of this domain. Requires the Google identity provider.",
  },
];

const PROVIDER_OF: Partial<Record<ClientGroupRuleType, { id: string; name: string }>> = {
  IDP_TENANT: { id: "microsoft", name: "Microsoft" },
  IDP_HOSTED_DOMAIN: { id: "google", name: "Google" },
};

const DOMAIN_RE = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/i;
const GUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

type RuleForm = { type: ClientGroupRuleType; list: string; include_subdomains: boolean; note?: string };

const splitList = (s: string, lower = false): string[] =>
  Array.from(new Set(s.split(/[\n,;\s]+/).map((x) => (lower ? x.trim().toLowerCase() : x.trim())).filter(Boolean)));

function ruleToForm(r: ClientGroupRule): RuleForm {
  const list = r.type === "IDP_TENANT" ? r.value.tenant_ids ?? [] : r.value.domains ?? [];
  return { type: r.type, list: list.join("\n"), include_subdomains: !!r.value.include_subdomains };
}
function ruleFromForm(f: RuleForm): ClientGroupRule {
  switch (f.type) {
    case "EMAIL_DOMAIN":
      return { type: "EMAIL_DOMAIN", value: { domains: splitList(f.list, true), include_subdomains: f.include_subdomains } };
    case "IDP_TENANT":
      return { type: "IDP_TENANT", value: { provider: "microsoft", tenant_ids: splitList(f.list) } };
    case "IDP_HOSTED_DOMAIN":
      return { type: "IDP_HOSTED_DOMAIN", value: { provider: "google", domains: splitList(f.list, true) } };
  }
}

function ruleProblem(r: RuleForm, n: string): string | undefined {
  const items = splitList(r.list);
  if (items.length === 0) return `${n}: enter at least one ${r.type === "IDP_TENANT" ? "tenant ID" : "domain"}.`;
  if (r.type === "IDP_TENANT") {
    const bad = items.find((x) => !GUID_RE.test(x));
    return bad ? `${n}: "${bad}" is not a tenant ID. A tenant ID looks like 00000000-0000-0000-0000-000000000000.` : undefined;
  }
  const bad = items.find((x) => !DOMAIN_RE.test(x));
  return bad ? `${n}: "${bad}" is not a domain. Enter a domain such as company.com, without @ or https://.` : undefined;
}

type FormState = {
  name: string;
  description: string;
  priority: string;
  enabled: boolean;
  rules: RuleForm[];
  reason: string;
};
const EMPTY: FormState = {
  name: "", description: "", priority: String(PRIORITY_DEFAULT), enabled: true,
  rules: [{ type: "EMAIL_DOMAIN", list: "", include_subdomains: false }], reason: "",
};
type FormErrors = { name?: string; priority?: string; rules: Record<number, string>; empty?: string };
const NO_ERRORS: FormErrors = { rules: {} };

const ACTION_WORDS: Record<ClientGroupChange["action"], string> = { CREATED: "Created", UPDATED: "Changed", DELETED: "Deleted" };

function groupDiff(c: ClientGroupChange) {
  const b = c.before ?? {};
  const a = c.after ?? {};
  const onOff = (v: boolean | undefined) => (v === undefined ? undefined : v ? "On" : "Off");
  const rules = (g: Partial<ClientGroup>) => (g.rules ? ruleSummary(g.rules) : undefined);
  const rows = [
    { field: "Name", before: b.name, after: a.name },
    { field: "Description", before: b.description ?? undefined, after: a.description ?? undefined },
    { field: "Priority", before: b.priority?.toString(), after: a.priority?.toString() },
    { field: "State", before: onOff(b.enabled), after: onOff(a.enabled) },
    { field: "Who is in it", before: rules(b), after: rules(a) },
  ];
  return rows.filter((r) => r.before !== r.after);
}

function RuleDetail({ rule }: { rule: ClientGroupRule }) {
  const kind = RULE_KINDS.find((k) => k.type === rule.type);
  const items = rule.type === "IDP_TENANT" ? rule.value.tenant_ids ?? [] : rule.value.domains ?? [];
  return (
    <li className="space-y-1.5 rounded-md border border-border px-3.5 py-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{kind?.label ?? rule.type}</span>
        <Badge tone={rule.type === "EMAIL_DOMAIN" ? "info" : "accent"}>{kind?.assurance ?? "Rule"}</Badge>
      </div>
      <ul className="space-y-0.5 font-mono text-xs" dir="ltr">
        {items.map((x) => <li key={x} className="break-all">{x}</li>)}
      </ul>
      {rule.type === "EMAIL_DOMAIN" && rule.value.include_subdomains && (
        <p className="text-caption text-muted-foreground">Subdomains count too.</p>
      )}
    </li>
  );
}

export default function ClientGroupsPage() {
  const toast = useToast();
  const caps = useCapabilities();
  const socialMod = moduleLicensed(caps, "social_login");
  const [rows, setRows] = useState<ClientGroup[] | null>(null);
  const roles = useOperatorRoles();
  const [err, setErr] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);
  const [errors, setErrors] = useState<FormErrors>(NO_ERRORS);
  const [emptyOk, setEmptyOk] = useState(false);

  const [mode, setMode] = useState<"closed" | "new" | "edit">("closed");
  const [editing, setEditing] = useState<ClientGroup | null>(null);
  const [viewing, setViewing] = useState<ClientGroup | null>(null);
  const [f, setF] = useState<FormState>(EMPTY);
  const [initial, setInitial] = useState<FormState>(EMPTY);
  const [deleting, setDeleting] = useState<ClientGroup | null>(null);
  const [deleteReason, setDeleteReason] = useState("");

  const [tab, setTab] = useState<"groups" | "history">("groups");
  const [changes, setChanges] = useState<ClientGroupChange[] | null>(null);
  const [changesErr, setChangesErr] = useState<unknown>(null);
  const [changesLoading, setChangesLoading] = useState(false);
  const [openChange, setOpenChange] = useState<ClientGroupChange | null>(null);

  const [providers, setProviders] = useState<string[] | null>(null);

  const writable = roles !== null && canWrite("client-groups", roles);
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF((p) => ({ ...p, [k]: v }));
  const setRule = (i: number, patch: Partial<RuleForm>) =>
    setF((p) => ({ ...p, rules: p.rules.map((r, j) => (j === i ? { ...r, ...patch } : r)) }));

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setRows((await api.get<ListResp<ClientGroup>>("/client-groups")).data ?? []);
      setErr(null);
    } catch (e) {
      setErr(e);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const loadChanges = useCallback(async () => {
    setChangesLoading(true);
    try {
      setChanges((await api.get<ListResp<ClientGroupChange>>("/client-groups/changes")).data ?? []);
      setChangesErr(null);
    } catch (e) {
      setChangesErr(e);
    } finally {
      setChangesLoading(false);
    }
  }, []);
  useEffect(() => { if (tab === "history") void loadChanges(); }, [tab, loadChanges]);

  const providersReadable = socialMod && roles !== null && canRead("social-providers", roles);
  useEffect(() => {
    if (mode === "closed" || !providersReadable) return;
    let live = true;
    api.get<ListResp<{ provider: string; enabled: boolean }>>("/social-providers")
      .then((r) => { if (live) setProviders((r.data ?? []).filter((p) => p.enabled).map((p) => p.provider)); })
      .catch(() => { if (live) setProviders(null); });
    return () => { live = false; };
  }, [mode, providersReadable]);

  const sorted = useMemo(
    () => (rows ?? []).slice().sort((a, b) => a.priority - b.priority || a.name.localeCompare(b.name)),
    [rows]);
  const nameOf = useCallback((id: string) => rows?.find((g) => g.id === id)?.name, [rows]);

  function resetForm(next: FormState) {
    setF(next); setInitial(next); setFormErr(null); setErrors(NO_ERRORS); setEmptyOk(false);
  }
  function openNew() {
    resetForm(EMPTY); setEditing(null); setMode("new");
  }
  function openEdit(g: ClientGroup) {
    resetForm({
      name: g.name, description: g.description ?? "", priority: String(g.priority), enabled: g.enabled,
      rules: (g.rules ?? []).length ? g.rules.map(ruleToForm) : [{ type: "EMAIL_DOMAIN", list: "", include_subdomains: false }],
      reason: "",
    });
    setViewing(null);
    setEditing(g); setMode("edit");
  }

  const dirty = useMemo(() => JSON.stringify(f) !== JSON.stringify(initial), [f, initial]);

  function changeKind(i: number, type: ClientGroupRuleType) {
    const r = f.rules[i];
    const sameShape = (r.type === "IDP_TENANT") === (type === "IDP_TENANT");
    const cleared = !sameShape && r.list.trim() !== "";
    setRule(i, {
      type,
      list: sameShape ? r.list : "",
      include_subdomains: type === "EMAIL_DOMAIN" ? r.include_subdomains : false,
      note: cleared ? "The list was cleared: tenant IDs and domains are not interchangeable." : undefined,
    });
    setErrors((e) => ({ ...e, rules: { ...e.rules, [i]: "" } }));
  }

  function focusFirstError() {
    requestAnimationFrame(() => {
      const el = document.querySelector<HTMLElement>('[role="dialog"] [aria-invalid="true"]');
      el?.scrollIntoView?.({ block: "center" });
      el?.focus();
    });
  }

  async function onSubmit() {
    setFormErr(null);
    const prio = Number(f.priority);
    const next: FormErrors = { rules: {} };
    if (!f.name.trim()) next.name = "Give the group a name.";
    if (f.priority.trim() === "" || !Number.isInteger(prio) || prio < PRIORITY_MIN || prio > PRIORITY_MAX) {
      next.priority = `Priority must be a whole number between ${PRIORITY_MIN} and ${PRIORITY_MAX}.`;
    }
    f.rules.forEach((r, i) => {
      const p = ruleProblem(r, `Rule ${i + 1}`);
      if (p) next.rules[i] = p;
    });
    if (f.rules.length === 0 && !emptyOk) {
      next.empty = `This group has no rules, so it matches nobody. Select ${mode === "new" ? "Create group" : "Save changes"} again to save it anyway.`;
      setEmptyOk(true);
    }
    setErrors(next);
    if (next.name || next.priority || Object.keys(next.rules).length > 0 || next.empty) {
      focusFirstError();
      return;
    }
    setBusy(true);
    const body = {
      name: f.name.trim(),
      description: f.description.trim() || undefined,
      priority: prio,
      enabled: f.enabled,
      rules: f.rules.map(ruleFromForm),
      reason: f.reason.trim() || undefined,
    };
    try {
      if (mode === "new") await api.post("/client-groups", body);
      else if (editing) await api.patch(`/client-groups/${editing.id}`, body);
      toast.success(mode === "new" ? "Group created" : "Group saved", "Membership is decided at the client's next sign-in.");
      setMode("closed"); setEditing(null);
      await load();
      if (tab === "history") await loadChanges();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onDelete() {
    if (!deleting) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/client-groups/${deleting.id}`, deleteReason.trim() ? { reason: deleteReason.trim() } : undefined);
      setDeleting(null);
      toast.success("Group deleted");
      await load();
      if (tab === "history") await loadChanges();
    } catch (e) {
      setFormErr(e instanceof ApiError && e.code === "in_use" ? e.message : e);
    }
    finally { setBusy(false); }
  }

  const changeName = (c: ClientGroupChange) => c.after?.name ?? c.before?.name ?? nameOf(c.group_id) ?? "A deleted group";

  const providerWarning = (type: ClientGroupRuleType): React.ReactNode => {
    const p = PROVIDER_OF[type];
    if (!p || caps === null) return null;
    if (!socialMod) {
      return <>Identity providers are not licensed on this site, so this rule matches nobody.</>;
    }
    if (providers === null || providers.includes(p.id)) return null;
    return (
      <>
        The {p.name} identity provider is not set up, so this rule matches nobody until it is set up under{" "}
        <Link href="/social-providers" className="underline underline-offset-2">Identity providers</Link>.
      </>
    );
  };

  const loadFailed = rows === null && err !== null;

  return (
    <PageShell>
      <PageHeader
        title="Client groups"
        description="Who a client is (staff, partners, VIPs), from a verified identity, so packages can target them."
        help={
          <>
            <HelpSection title="What a group is">
              <p>
                A group is a set of clients, decided from what they have <strong>verified</strong>: an email address at a
                domain, or an account in an organisation&apos;s Microsoft or Google directory. A client is in the group
                when any one of its rules matches. Voucher, client-account and room sign-ins carry no identity and are
                never in a group.
              </p>
              <p>
                Packages then name a group as their <strong>audience</strong>. For example: an <em>Employees</em> group
                (Microsoft organisation), a <em>Partners</em> group (verified email at partner domains), a Free package
                offered only to Employees, a discounted package offered only to Partners, and the normal packages offered
                to public clients. There is no discount engine: a cheaper package with a narrower audience is the
                discount.
              </p>
            </HelpSection>
            <HelpSection title="Two levels of assurance">
              <HelpList
                items={[
                  <><strong>Verified email address domain</strong> — anyone who can receive mail at the domain qualifies, including someone whose old mailbox is still forwarded. Right for a commercial benefit such as partner pricing.</>,
                  <><strong>Organisation account</strong> (Microsoft Entra tenant or Google Workspace) — the client is in the organisation&apos;s directory <em>now</em>. Right for an employee benefit. Needs that identity provider set up under Identity providers.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Priority">
              <p>
                When a client matches several groups, the group with the <strong>lowest number</strong> wins and is the one
                packages see. A switched-off group matches nobody, and so does a group with no rules.
              </p>
            </HelpSection>
          </>
        }
        actions={writable && <Button onClick={openNew}><Plus /> Add group</Button>}
      />

      {roles !== null && !writable && <ReadOnlyNotice>Your role can see the client groups but not change them.</ReadOnlyNotice>}
      {rows !== null && <ErrorBanner err={err} className="mb-0" />}

      <Tabs value={tab} onValueChange={(v) => setTab(v as "groups" | "history")}>
        <TabsList>
          <TabsTrigger value="groups">Groups</TabsTrigger>
          <TabsTrigger value="history">History</TabsTrigger>
        </TabsList>

        <TabsContent value="groups" className="mt-5">
          <Card className="overflow-hidden">
              {rows === null ? (
                loadFailed ? (
                  <EmptyState
                    icon={<AlertTriangle />}
                    title="Client groups could not be loaded"
                    hint="Nothing has been changed."
                    action={
                      <Button variant="secondary" disabled={loading} onClick={() => void load()}>
                        <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
                      </Button>
                    }
                  />
                ) : <SkeletonRows rows={3} cols={5} />
              ) : rows.length === 0 ? (
                <EmptyState
                  icon={<UsersRound />}
                  title="No client groups yet"
                  hint={
                    <span className="inline-flex items-center gap-1">
                      Offer staff, partners or VIPs their own packages.
                      <InfoHint>
                        For example an <strong>Employees</strong> group matched by your Microsoft or Google organisation and
                        given a Free package, a <strong>Partners</strong> group matched by verified email at their domains
                        and given a cheaper package, with public clients seeing the normal packages.
                      </InfoHint>
                    </span>
                  }
                  action={writable ? <Button onClick={openNew}><Plus /> Add the first group</Button> : undefined}
                />
              ) : (
                <Table label="Client groups">
                  <THead>
                    <TR>
                      <TH>Group</TH>
                      <TH numeric className="hidden sm:table-cell">Priority</TH>
                      <TH>Who is in it</TH>
                      <TH>State</TH>
                      <TH><span className="sr-only">Actions</span></TH>
                    </TR>
                  </THead>
                  <TBody>
                    {sorted.map((g) => (
                      <TR key={g.id} onClick={() => setViewing(g)}>
                        <TD className="max-w-[16rem]">
                          <div className="truncate font-medium" title={g.name}>{g.name}</div>
                          {g.description && <div className="truncate text-xs text-muted-foreground" title={g.description}>{g.description}</div>}
                          <div className="text-xs text-muted-foreground tabular sm:hidden">Priority {g.priority}</div>
                        </TD>
                        <TD numeric className="hidden sm:table-cell">{g.priority}</TD>
                        <TD truncate muted title={ruleSummary(g.rules)} className="max-w-[10rem] sm:max-w-md">{ruleSummary(g.rules)}</TD>
                        <TD nowrap>{g.enabled ? <StatusBadge tone="ok">On</StatusBadge> : <StatusBadge>Off</StatusBadge>}</TD>
                        <TD className="w-0">
                          <RowActions
                            subject={g.name}
                            primary={{ label: "Details", onSelect: () => setViewing(g) }}
                            items={writable ? [
                              { label: "Edit", icon: <Pencil />, onSelect: () => openEdit(g) },
                              { label: "Delete…", danger: true, icon: <Trash2 />,
                                onSelect: () => { setFormErr(null); setDeleteReason(""); setDeleting(g); } },
                            ] : undefined}
                          />
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              )}
          </Card>
          {rows !== null && rows.length > 0 && (
            <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
              <span className="inline-flex items-center gap-1">
                Lowest priority number wins
                <InfoHint>Listed in the order the appliance decides them: when a client matches several groups, the lowest number wins.</InfoHint>
              </span>
              <Link href="/internet-packages" className="inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline">
                Choose audiences in Internet packages <ArrowUpRight className="size-3" aria-hidden />
              </Link>
            </div>
          )}
        </TabsContent>

        <TabsContent value="history" className="mt-5">
          <Card className="overflow-hidden">
            {changes !== null && <ErrorBanner err={changesErr} className="m-4" />}
            {changes === null ? (
              changesErr ? (
                <EmptyState
                  icon={<AlertTriangle />}
                  title="The history could not be loaded"
                  hint="Nothing has been changed."
                  action={
                    <Button variant="secondary" disabled={changesLoading} onClick={() => void loadChanges()}>
                      <RefreshCw className={cn(changesLoading && "animate-spin motion-reduce:animate-none")} /> Try again
                    </Button>
                  }
                />
              ) : <SkeletonRows rows={3} cols={4} />
            ) : changes.length === 0 ? (
              <EmptyState size="sm" title="No changes recorded" hint="Changes to groups appear here." />
            ) : (
              <HistoryList
                label="Client group history"
                onSelect={(it) => setOpenChange(changes.find((c) => c.id === it.id) ?? null)}
                selectedId={openChange?.id ?? null}
                items={changes.map((c) => ({
                  id: c.id,
                  title: <span>{changeName(c)}</span>,
                  badge: <Badge tone={c.action === "DELETED" ? "err" : c.action === "CREATED" ? "ok" : "info"}>{ACTION_WORDS[c.action] ?? c.action}</Badge>,
                  actor: <span>{c.changed_by || "—"}</span>,
                  at: c.changed_at,
                  summary: c.reason || undefined,
                  tone: c.action === "DELETED" ? "err" : c.action === "CREATED" ? "ok" : "info",
                }))}
              />
            )}
          </Card>
        </TabsContent>
      </Tabs>

      <DetailsDrawer
        open={viewing !== null}
        onOpenChange={(v) => !v && setViewing(null)}
        eyebrow="Client group"
        title={viewing?.name ?? ""}
        description={viewing?.description || undefined}
        badges={viewing ? (viewing.enabled ? <StatusBadge tone="ok">On</StatusBadge> : <StatusBadge>Off</StatusBadge>) : undefined}
        actions={writable && viewing ? <Button onClick={() => openEdit(viewing)}><Pencil /> Edit</Button> : undefined}
      >
        {viewing && (
          <>
            <DetailsSection title="Who is in it">
              {(viewing.rules ?? []).length === 0 ? (
                <p className="text-sm text-muted-foreground">No rules, so this group matches nobody.</p>
              ) : (
                <ul className="space-y-2">
                  {viewing.rules.map((r, i) => <RuleDetail key={r.id ?? i} rule={r} />)}
                </ul>
              )}
              <p className="mt-2 text-caption text-muted-foreground">Any one rule matching puts the client in the group.</p>
            </DetailsSection>
            <DetailsSection title="Settings">
              <DescriptionList items={[
                { label: "Priority", value: <span className="tabular">{viewing.priority}</span> },
                { label: "State", value: viewing.enabled ? "On" : "Off, so it matches nobody" },
                { label: "Last changed", value: formatDate(viewing.updated_at) },
              ]} />
            </DetailsSection>
          </>
        )}
      </DetailsDrawer>

      <DetailsDrawer
        open={openChange !== null}
        onOpenChange={(v) => !v && setOpenChange(null)}
        eyebrow="Client group change"
        title={openChange ? changeName(openChange) : ""}
        badges={openChange ? <Badge tone={openChange.action === "DELETED" ? "err" : openChange.action === "CREATED" ? "ok" : "info"}>{ACTION_WORDS[openChange.action] ?? openChange.action}</Badge> : undefined}
      >
        {openChange && (
          <>
            <DetailsSection title="What changed">
              <ChangeDiff changes={groupDiff(openChange)} />
            </DetailsSection>
            <DetailsSection title="Record">
              <DescriptionList items={[
                { label: "When", value: formatDate(openChange.changed_at) },
                { label: "Changed by", value: openChange.changed_by || "—" },
                { label: "Reason", value: openChange.reason || "—" },
              ]} />
            </DetailsSection>
          </>
        )}
      </DetailsDrawer>

      <DialogForm
        open={mode !== "closed"}
        onOpenChange={(v) => { if (!v) { setMode("closed"); setEditing(null); } }}
        title={mode === "edit" ? `Edit ${editing?.name ?? "group"}` : "Add a client group"}
        description="Clients join when any rule matches a verified identity."
        submitLabel={mode === "edit" ? "Save changes" : "Create group"}
        size="lg"
        busy={busy}
        error={formErr}
        dirty={dirty}
        onSubmit={onSubmit}
      >
        <QuietHints>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name" required error={errors.name}>
            <Input value={f.name} onChange={(e) => { set("name", e.target.value); setErrors((x) => ({ ...x, name: undefined })); }}
              placeholder="Employees" required />
          </Field>
          <Field
            label="Priority"
            hint={`When a client matches several groups, the lowest number wins. Default ${PRIORITY_DEFAULT}; allowed ${PRIORITY_MIN}–${PRIORITY_MAX}.`}
            error={errors.priority}
          >
            <Input type="number" min={PRIORITY_MIN} max={PRIORITY_MAX} step={1} value={f.priority}
              onChange={(e) => { set("priority", e.target.value); setErrors((x) => ({ ...x, priority: undefined })); }} />
          </Field>
          <Field label="Description" className="sm:col-span-2">
            <Input value={f.description} onChange={(e) => set("description", e.target.value)} placeholder="Optional" />
          </Field>
        </div>

        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-1">
              <span className="text-label">Membership rules</span>
              
            </div>
            <Button type="button" variant="ghost" size="sm"
              onClick={() => { set("rules", [...f.rules, { type: "EMAIL_DOMAIN", list: "", include_subdomains: false }]); setEmptyOk(false); setErrors((x) => ({ ...x, empty: undefined })); }}>
              <Plus /> Add rule
            </Button>
          </div>
          {f.rules.length === 0 && (
            <p className={cn("text-xs", errors.empty ? "text-destructive" : "text-warning-subtle-foreground", FADE)} role={errors.empty ? "alert" : "status"}>
              {errors.empty ?? "A group with no rules matches nobody."}
            </p>
          )}
          {f.rules.map((r, i) => {
            const kind = RULE_KINDS.find((k) => k.type === r.type) ?? RULE_KINDS[0];
            const n = `Rule ${i + 1}`;
            const warn = providerWarning(r.type);
            return (
              <div key={i} className={cn("space-y-3 rounded-md border border-border bg-surface/40 p-3.5", FADE)} data-testid={`group-rule-${i}`}>
                <div className="flex items-start gap-2">
                  <Field label={`${n}: kind`} className="flex-1">
                    <Select value={r.type} aria-label={`${n}: kind`} data-testid={`group-rule-type-${i}`}
                      onChange={(e) => changeKind(i, e.target.value as ClientGroupRuleType)}>
                      {RULE_KINDS.map((k) => <option key={k.type} value={k.type}>{k.label}</option>)}
                    </Select>
                  </Field>
                  <Button type="button" variant="ghost" size="icon" className="mt-6" aria-label={`Remove ${n.toLowerCase()}`}
                    onClick={() => {
                      set("rules", f.rules.filter((_, j) => j !== i));
                      setErrors((x) => ({ ...x, rules: {} }));
                    }}>
                    <Trash2 />
                  </Button>
                </div>
                <div className="flex flex-wrap items-center gap-1">
                  <Badge tone={r.type === "EMAIL_DOMAIN" ? "info" : "accent"}>{kind.assurance}</Badge>
                  <InfoHint>{kind.hint}</InfoHint>
                </div>
                {warn && <p className={cn("text-xs text-warning-subtle-foreground", FADE)} role="status">{warn}</p>}
                <Field
                  label={r.type === "IDP_TENANT" ? `${n}: tenant ID(s)` : `${n}: domain(s)`}
                  required
                  error={errors.rules[i] || undefined}
                  hint={r.note ?? (r.type === "IDP_TENANT"
                    ? "The directory (tenant) ID from Microsoft Entra, a GUID. One per line."
                    : "One per line, for example company.com. Matched against the verified address, case-insensitively.")}
                >
                  <Textarea value={r.list} data-testid={`group-rule-list-${i}`} dir="ltr" required
                    placeholder={r.type === "IDP_TENANT" ? "00000000-0000-0000-0000-000000000000" : "company.com\ncompany.ae"}
                    onChange={(e) => {
                      setRule(i, { list: e.target.value, note: undefined });
                      setErrors((x) => ({ ...x, rules: { ...x.rules, [i]: "" } }));
                    }} />
                </Field>
                {r.type === "EMAIL_DOMAIN" && (
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex items-center gap-1">
                      <span className="text-sm font-medium">Include subdomains</span>
                      <InfoHint>mail.company.com counts as company.com.</InfoHint>
                    </div>
                    <Switch checked={r.include_subdomains} onCheckedChange={(v) => setRule(i, { include_subdomains: v })}
                      label={`${n}: include subdomains`} />
                  </div>
                )}
              </div>
            );
          })}
        </div>

        <div className="flex items-center justify-between rounded-md border border-border bg-surface/50 px-3.5 py-2.5">
          <div className="flex items-center gap-1">
            <span className="text-sm font-medium">Group is on</span>
            <InfoHint>A switched-off group matches nobody; packages that name it are offered to no one through it.</InfoHint>
          </div>
          <Switch checked={f.enabled} onCheckedChange={(v) => set("enabled", v)} label="Group is on" />
        </div>

        <Field label="Reason for this change">
          <Input value={f.reason} onChange={(e) => set("reason", e.target.value)} placeholder="Partner pricing agreed with Acme" />
        </Field>
        </QuietHints>
      </DialogForm>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title={deleting ? `Delete ${deleting.name}?` : "Delete this group?"}
        description="From their next sign-in, its clients see the packages everyone else sees. A package that targets this group must be changed first."
        confirmLabel="Delete"
        confirmVariant="danger"
        busy={busy}
        error={formErr}
        onConfirm={onDelete}
      >
        <Field label="Reason (optional)">
          <Input value={deleteReason} onChange={(e) => setDeleteReason(e.target.value)} placeholder="No longer a partner" />
        </Field>
      </ConfirmDialog>
    </PageShell>
  );
}
