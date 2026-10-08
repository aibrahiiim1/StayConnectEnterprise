"use client";

// CLIENT GROUPS — who a client IS, as opposed to how they signed in.
//
// A group is a site-level policy object: a name, a priority, and one or more membership rules. A client is a
// member when ANY rule matches, membership is decided from the client's VERIFIED identity (a mailbox proven by
// a code or a trusted identity provider; an organisation asserted by Microsoft or Google), and a client who
// matches several groups gets the one with the lowest priority number. Packages then name a group as their
// AUDIENCE: "Employees free, partners discounted, public normal" is three packages and two groups, with no
// discount engine and no rule about Google or email anywhere (docs/architecture/
// ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §3, §4).
//
// TWO LEVELS OF ASSURANCE, SAID PLAINLY. A verified email address at company.com is right for a commercial
// benefit (anyone who can receive mail there qualifies, including an ex-employee whose mailbox is still
// forwarded); an organisation account answers "is this person in the directory NOW", which is what an employee
// benefit actually asks. The rule editor names both so a site admin can choose without being an IAM engineer.
//
// Same treatment as the other list screens: the editor is a dialog, delete is a real confirmation, and a group
// that a package still names as its audience is refused by the server (409) -- the refusal says which packages,
// and this screen shows it where the operator is looking.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ApiError, ListResp, Whoami, ClientGroup, ClientGroupChange, ClientGroupRule, ClientGroupRuleType } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select, Textarea } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { Switch, SkeletonRows } from "@/components/ui/misc";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Plus, Trash2, UsersRound, ArrowUpRight } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { formatDate, formatRelative } from "@/lib/utils";

// Priority: an operational number with a default, bounds and an explanation (lower wins).
const PRIORITY_DEFAULT = 100;
const PRIORITY_MIN = 1;
const PRIORITY_MAX = 1000;

// THE RULE KINDS, in the operator's words, each with its assurance level stated. The order is deliberate:
// the commonest first, then the two strong ones.
const RULE_KINDS: { type: ClientGroupRuleType; label: string; assurance: string; hint: string }[] = [
  {
    type: "EMAIL_DOMAIN",
    label: "Verified email address domain",
    assurance: "Verified email address",
    hint: "Suitable for commercial benefits such as partner pricing. Anyone who can receive mail at the domain qualifies. The address is verified by a one-time code, or by a Google, Apple or Microsoft sign-in that asserts it.",
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
const RULE_LABEL: Record<ClientGroupRuleType, string> = Object.fromEntries(RULE_KINDS.map((k) => [k.type, k.label])) as Record<ClientGroupRuleType, string>;

// One rule as the editor holds it: the list fields are typed as text (one per line, or comma-separated) and
// split on save, so an operator can paste a list from anywhere.
type RuleForm = { type: ClientGroupRuleType; list: string; include_subdomains: boolean };

// One per line, or comma-separated; a domain repeated in another case is kept once, because the engine matches
// case-insensitively and a second spelling would add nothing. Tenant IDs are GUIDs and are kept as typed.
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

/** "company.com, company.ae · Microsoft tenant" — the group's rules in one line for the table. */
export function ruleSummary(rules: ClientGroupRule[] | null | undefined): string {
  if (!rules || rules.length === 0) return "No rules — matches nobody";
  return rules.map((r) => {
    switch (r.type) {
      case "EMAIL_DOMAIN": {
        const d = (r.value.domains ?? []).join(", ");
        return r.value.include_subdomains ? `${d} (and subdomains)` : d;
      }
      case "IDP_TENANT": return (r.value.tenant_ids ?? []).length > 1 ? `${r.value.tenant_ids!.length} Microsoft tenants` : "Microsoft tenant";
      case "IDP_HOSTED_DOMAIN": return `Google Workspace ${(r.value.domains ?? []).join(", ")}`;
      default: return String((r as { type: string }).type);
    }
  }).join(" · ");
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

const ACTION_WORDS: Record<ClientGroupChange["action"], string> = { CREATED: "Created", UPDATED: "Changed", DELETED: "Deleted" };

export default function ClientGroupsPage() {
  const toast = useToast();
  const [rows, setRows] = useState<ClientGroup[] | null>(null);
  const [roles, setRoles] = useState<string[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);

  const [mode, setMode] = useState<"closed" | "new" | "edit">("closed");
  const [editing, setEditing] = useState<ClientGroup | null>(null);
  const [f, setF] = useState<FormState>(EMPTY);
  const [deleting, setDeleting] = useState<ClientGroup | null>(null);
  const [deleteReason, setDeleteReason] = useState("");

  const [tab, setTab] = useState<"groups" | "history">("groups");
  const [changes, setChanges] = useState<ClientGroupChange[] | null>(null);
  const [changesErr, setChangesErr] = useState<unknown>(null);

  const writable = roles !== null && canWrite("client-groups", roles);
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF((p) => ({ ...p, [k]: v }));
  const setRule = (i: number, patch: Partial<RuleForm>) =>
    setF((p) => ({ ...p, rules: p.rules.map((r, j) => (j === i ? { ...r, ...patch } : r)) }));

  const load = useCallback(async () => {
    try { setRows((await api.get<ListResp<ClientGroup>>("/client-groups")).data ?? []); }
    catch (e) { setErr(e); }
  }, []);
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, [load]);

  // The history is read when its tab is opened, and again after every write, so what it shows is what happened.
  const loadChanges = useCallback(async () => {
    setChangesErr(null);
    try { setChanges((await api.get<ListResp<ClientGroupChange>>("/client-groups/changes")).data ?? []); }
    catch (e) { setChangesErr(e); setChanges([]); }
  }, []);
  useEffect(() => { if (tab === "history") loadChanges(); }, [tab, loadChanges]);

  // Sorted as the engine decides: by priority, then name. The table order IS the conflict order.
  const sorted = useMemo(
    () => (rows ?? []).slice().sort((a, b) => a.priority - b.priority || a.name.localeCompare(b.name)),
    [rows]);
  const nameOf = useCallback((id: string) => rows?.find((g) => g.id === id)?.name, [rows]);

  function openNew() {
    setF(EMPTY); setEditing(null); setFormErr(null); setMode("new");
  }
  function openEdit(g: ClientGroup) {
    setF({
      name: g.name, description: g.description ?? "", priority: String(g.priority), enabled: g.enabled,
      rules: (g.rules ?? []).length ? g.rules.map(ruleToForm) : [{ type: "EMAIL_DOMAIN", list: "", include_subdomains: false }],
      reason: "",
    });
    setEditing(g); setFormErr(null); setMode("edit");
  }

  async function onSubmit() {
    setFormErr(null);
    const prio = Number(f.priority);
    if (!f.name.trim()) { setFormErr("Give the group a name."); return; }
    if (!Number.isInteger(prio) || prio < PRIORITY_MIN || prio > PRIORITY_MAX) {
      setFormErr(`Priority must be a whole number between ${PRIORITY_MIN} and ${PRIORITY_MAX}.`); return;
    }
    for (const [i, r] of f.rules.entries()) {
      if (splitList(r.list).length === 0) {
        setFormErr(`Rule ${i + 1}: enter at least one ${r.type === "IDP_TENANT" ? "tenant ID" : "domain"}.`); return;
      }
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
      // 409 in_use: the server names the packages whose audience this group is. Shown verbatim, in the dialog.
      setFormErr(e instanceof ApiError && e.code === "in_use" ? e.message : e);
    }
    finally { setBusy(false); }
  }

  return (
    <PageShell>
      <PageHeader
        icon={<UsersRound />}
        eyebrow="Client access"
        title="Client groups"
        description="Who a client is — employees, partners, VIPs — decided from a verified identity, so a package can be offered to some clients and not others."
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
      <ErrorBanner err={err} className="mb-0" />

      <Tabs value={tab} onValueChange={(v) => setTab(v as "groups" | "history")}>
        <TabsList>
          <TabsTrigger value="groups">Groups</TabsTrigger>
          <TabsTrigger value="history">History</TabsTrigger>
        </TabsList>

        <TabsContent value="groups" className="mt-4">
          <Card>
            <CardBody className="p-0">
              {rows === null ? (
                <SkeletonRows rows={3} cols={5} />
              ) : rows.length === 0 ? (
                <EmptyState
                  icon={<UsersRound />}
                  title="No client groups yet"
                  hint={
                    <>
                      Every client is offered the same packages. Create a group to offer some clients something
                      different — for example an <strong>Employees</strong> group matched by your Microsoft or Google
                      organisation and given a Free package, a <strong>Partners</strong> group matched by verified email
                      at their domains and given a cheaper package, with public clients seeing the normal packages.
                    </>
                  }
                  action={writable ? <Button onClick={openNew}><Plus /> Add the first group</Button> : undefined}
                />
              ) : (
                <Table>
                  <THead>
                    <TR><TH>Group</TH><TH>Priority</TH><TH>Members</TH><TH>State</TH><TH /></TR>
                  </THead>
                  <TBody>
                    {sorted.map((g) => (
                      <TR key={g.id}>
                        <TD>
                          <div className="font-medium">{g.name}</div>
                          {g.description && <div className="text-xs text-muted-foreground">{g.description}</div>}
                        </TD>
                        <TD className="tabular text-sm">{g.priority}</TD>
                        <TD className="max-w-md text-sm text-muted-foreground">{ruleSummary(g.rules)}</TD>
                        <TD>{g.enabled ? <Badge tone="ok" dot>On</Badge> : <Badge tone="default">Off</Badge>}</TD>
                        <TD className="whitespace-nowrap text-right">
                          {writable && <Button size="sm" variant="ghost" onClick={() => openEdit(g)}>Edit</Button>}
                          {writable && (
                            <Button size="sm" variant="ghost" onClick={() => { setFormErr(null); setDeleteReason(""); setDeleting(g); }}>
                              Delete
                            </Button>
                          )}
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              )}
            </CardBody>
          </Card>
          {rows !== null && rows.length > 0 && (
            <p className="mt-3 text-xs text-muted-foreground">
              Listed in the order the appliance decides them: when a client matches several groups, the lowest number wins.
              Packages choose their audience under{" "}
              <Link href="/internet-packages" className="inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline">
                Internet packages <ArrowUpRight className="size-3" aria-hidden />
              </Link>.
            </p>
          )}
        </TabsContent>

        <TabsContent value="history" className="mt-4">
          <Card>
            <CardBody className="p-0">
              <ErrorBanner err={changesErr} className="m-4" />
              {changes === null ? (
                <SkeletonRows rows={3} cols={4} />
              ) : changes.length === 0 ? (
                <EmptyState title="No changes recorded" hint="Every creation, edit and deletion of a group is recorded here, with who made it and the reason given." />
              ) : (
                <Table>
                  <THead>
                    <TR><TH>When</TH><TH>What</TH><TH>Group</TH><TH>By</TH><TH>Reason</TH></TR>
                  </THead>
                  <TBody>
                    {changes.map((c) => {
                      const name = c.after?.name ?? c.before?.name ?? nameOf(c.group_id) ?? c.group_id;
                      return (
                        <TR key={c.id}>
                          <TD className="whitespace-nowrap text-sm text-muted-foreground" title={formatDate(c.changed_at)}>
                            {formatRelative(c.changed_at)}
                          </TD>
                          <TD>
                            <Badge tone={c.action === "DELETED" ? "err" : c.action === "CREATED" ? "ok" : "info"}>
                              {ACTION_WORDS[c.action] ?? c.action}
                            </Badge>
                          </TD>
                          <TD className="text-sm">{name}</TD>
                          <TD className="text-sm text-muted-foreground">{c.changed_by || "—"}</TD>
                          <TD className="text-sm text-muted-foreground">{c.reason || "—"}</TD>
                        </TR>
                      );
                    })}
                  </TBody>
                </Table>
              )}
            </CardBody>
          </Card>
        </TabsContent>
      </Tabs>

      <DialogForm
        open={mode !== "closed"}
        onOpenChange={(v) => { if (!v) { setMode("closed"); setEditing(null); } }}
        title={mode === "edit" ? `Edit ${editing?.name ?? "group"}` : "Add a client group"}
        description="A client is in the group when any one of its rules matches their verified identity."
        submitLabel={mode === "edit" ? "Save changes" : "Create group"}
        size="lg"
        busy={busy}
        error={formErr}
        onSubmit={onSubmit}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name" required hint="As it appears when a package chooses its audience.">
            <Input value={f.name} onChange={(e) => set("name", e.target.value)} placeholder="Employees" required />
          </Field>
          <Field
            label="Priority"
            hint={`When a client matches several groups, the lowest number wins. Default ${PRIORITY_DEFAULT}; allowed ${PRIORITY_MIN}–${PRIORITY_MAX}.`}
          >
            <Input type="number" min={PRIORITY_MIN} max={PRIORITY_MAX} step={1} value={f.priority}
              onChange={(e) => set("priority", e.target.value)} />
          </Field>
          <Field label="Description" hint="For the next operator. Clients never see it." className="sm:col-span-2">
            <Input value={f.description} onChange={(e) => set("description", e.target.value)} placeholder="Optional" />
          </Field>
        </div>

        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <div>
              <div className="text-label">Membership rules</div>
              <div className="text-xs text-muted-foreground">Any one rule matching puts the client in the group.</div>
            </div>
            <Button type="button" variant="ghost" size="sm"
              onClick={() => set("rules", [...f.rules, { type: "EMAIL_DOMAIN", list: "", include_subdomains: false }])}>
              <Plus /> Add rule
            </Button>
          </div>
          {f.rules.length === 0 && (
            <p className="text-xs text-warning-subtle-foreground" role="status">A group with no rules matches nobody.</p>
          )}
          {f.rules.map((r, i) => {
            const kind = RULE_KINDS.find((k) => k.type === r.type) ?? RULE_KINDS[0];
            const n = `Rule ${i + 1}`;
            return (
              <div key={i} className="space-y-3 rounded-md border border-border bg-surface/40 p-3.5" data-testid={`group-rule-${i}`}>
                <div className="flex items-start gap-2">
                  <Field label={`${n}: kind`} className="flex-1">
                    <Select value={r.type} aria-label={`${n}: kind`} data-testid={`group-rule-type-${i}`}
                      onChange={(e) => setRule(i, { type: e.target.value as ClientGroupRuleType, list: "", include_subdomains: false })}>
                      {RULE_KINDS.map((k) => <option key={k.type} value={k.type}>{k.label}</option>)}
                    </Select>
                  </Field>
                  <Button type="button" variant="ghost" size="icon" className="mt-6" aria-label={`Remove ${n.toLowerCase()}`}
                    onClick={() => set("rules", f.rules.filter((_, j) => j !== i))}>
                    <Trash2 />
                  </Button>
                </div>
                <div className="flex flex-wrap items-center gap-1.5">
                  <Badge tone={r.type === "EMAIL_DOMAIN" ? "info" : "accent"}>{kind.assurance}</Badge>
                  <span className="text-xs text-muted-foreground">{kind.hint}</span>
                </div>
                <Field
                  label={r.type === "IDP_TENANT" ? `${n}: tenant ID(s)` : `${n}: domain(s)`}
                  required
                  hint={r.type === "IDP_TENANT"
                    ? "The directory (tenant) ID from Microsoft Entra, a GUID. One per line."
                    : "One per line, for example company.com. Matched against the verified address, case-insensitively."}
                >
                  <Textarea value={r.list} data-testid={`group-rule-list-${i}`} dir="ltr" required
                    placeholder={r.type === "IDP_TENANT" ? "00000000-0000-0000-0000-000000000000" : "company.com\ncompany.ae"}
                    onChange={(e) => setRule(i, { list: e.target.value })} />
                </Field>
                {r.type === "EMAIL_DOMAIN" && (
                  <div className="flex items-center justify-between gap-3">
                    <div>
                      <div className="text-sm font-medium">Include subdomains</div>
                      <div className="text-xs text-muted-foreground">mail.company.com counts as company.com.</div>
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
          <div>
            <div className="text-sm font-medium">Group is on</div>
            <div className="text-xs text-muted-foreground">A switched-off group matches nobody; packages that name it are offered to no one through it.</div>
          </div>
          <Switch checked={f.enabled} onCheckedChange={(v) => set("enabled", v)} label="Group is on" />
        </div>

        <Field label="Reason for this change" hint="Optional. Recorded in the group's history and the activity log.">
          <Input value={f.reason} onChange={(e) => set("reason", e.target.value)} placeholder="Partner pricing agreed with Acme" />
        </Field>
      </DialogForm>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title={deleting ? `Delete ${deleting.name}?` : "Delete this group?"}
        description="Clients in this group are offered the same packages as everyone else from their next sign-in. A package that names this group as its audience must be changed first; the appliance refuses the deletion until it is."
        confirmLabel="Delete"
        confirmVariant="danger"
        busy={busy}
        error={formErr}
        onConfirm={onDelete}
      >
        <Field label="Reason" hint="Optional. Recorded in the group's history.">
          <Input value={deleteReason} onChange={(e) => setDeleteReason(e.target.value)} placeholder="No longer a partner" />
        </Field>
      </ConfirmDialog>
    </PageShell>
  );
}
