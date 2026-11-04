"use client";

// Custom roles: a name and, per feature, No access, View or Manage. edged decides what a custom role may hold (never more than a
// built-in role other than Site admin) and enforces it; the built-in roles are shown read-only.

import { useEffect, useMemo, useState } from "react";
import { Eye, Pencil, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { api, type CustomRole, type RoleFeature, type RolesResponse } from "@/lib/api";
import { canWrite, ROLE_LABELS, type SiteRole } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmDialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { RowActions } from "@/components/ui/row-actions";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Segmented } from "@/components/ui/tabs";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { HelpList, HelpSection } from "@/components/help";

type Level = "none" | "read" | "write";
const LEVEL_LABEL: Record<Level, string> = { none: "No access", read: "View", write: "Manage" };

type Draft = { id?: string; name: string; description: string; perms: Record<string, Level> };

function groupsOf(features: RoleFeature[]): [string, RoleFeature[]][] {
  const out: [string, RoleFeature[]][] = [];
  for (const f of features) {
    const g = out.find(([name]) => name === f.group);
    if (g) g[1].push(f);
    else out.push([f.group, [f]]);
  }
  return out;
}

const count = (perms: Record<string, string>) => Object.values(perms).filter((v) => v === "read" || v === "write").length;

export default function RolesPage() {
  const toast = useToast();
  const roles = useOperatorRoles();
  const writable = roles !== null && canWrite("roles", roles);
  const [data, setData] = useState<RolesResponse | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [viewing, setViewing] = useState<{ title: string; perms: Record<string, string>; all?: boolean } | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState<CustomRole | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);

  async function load() {
    try { setData(await api.get<RolesResponse>("/roles")); setErr(null); }
    catch (e) { setErr(e); }
  }
  useEffect(() => { void load(); }, []);

  const groups = useMemo(() => groupsOf(data?.features ?? []), [data]);
  const chosen = draft ? count(draft.perms) : 0;

  function openNew() {
    setActionErr(null);
    setDraft({ name: "", description: "", perms: {} });
  }
  function openEdit(r: CustomRole) {
    setActionErr(null);
    setDraft({ id: r.id, name: r.name, description: r.description, perms: { ...r.permissions } });
  }
  function setLevel(key: string, level: Level) {
    setDraft((d) => (d ? { ...d, perms: { ...d.perms, [key]: level } } : d));
  }
  function setGroup(features: RoleFeature[], level: Level) {
    setDraft((d) => {
      if (!d || !data) return d;
      const perms = { ...d.perms };
      for (const f of features) {
        const max = data.max[f.key] ?? "none";
        perms[f.key] = level === "write" && max !== "write" ? (max === "read" ? "read" : "none") : level === "read" && max === "none" ? "none" : level;
      }
      return { ...d, perms };
    });
  }

  async function save({ password }: { password: string }) {
    if (!draft) return;
    setBusy(true); setActionErr(null);
    const permissions = Object.fromEntries(Object.entries(draft.perms).filter(([, v]) => v !== "none"));
    const body = { name: draft.name.trim(), description: draft.description.trim(), permissions, password };
    try {
      if (draft.id) await api.patch(`/roles/${draft.id}`, body);
      else await api.post("/roles", body);
      toast.success(draft.id ? "Role saved" : "Role created", draft.id
        ? "Everyone who holds it has the new permissions from their next action."
        : "Give it to staff from Operators.");
      setConfirming(false); setDraft(null);
      await load();
    } catch (e) { setActionErr(e); } finally { setBusy(false); }
  }

  async function remove({ password }: { password: string }) {
    if (!deleting) return;
    setBusy(true); setActionErr(null);
    try {
      await api.del(`/roles/${deleting.id}`, { password });
      toast.success("Role removed");
      setDeleting(null);
      await load();
    } catch (e) { setActionErr(e); } finally { setBusy(false); }
  }

  return (
    <PageShell>
      <PageHeader
        title="Roles"
        description="Choose what each staff role can see and change."
        actions={writable && <Button onClick={openNew}><Plus /> New role</Button>}
        help={
          <>
            <HelpSection title="Custom roles">
              <HelpList items={[
                "For each feature, choose No access, View (see it) or Manage (see and change it).",
                "A custom role can never do more than a built-in role: what only Site admin can do stays with Site admin.",
                "Give a role to staff from Operators. A change to a role applies to everyone who holds it straight away.",
                "Creating, changing or removing a role asks for your password and is recorded in Activity.",
              ]} />
            </HelpSection>
          </>
        }
      />
      <ErrorBanner err={err} />
      {roles !== null && !writable && <ReadOnlyNotice>Only a Site admin manages roles.</ReadOnlyNotice>}

      <Card>
        <CardHeader><CardTitle>Custom roles</CardTitle></CardHeader>
        {data === null ? (
          <CardBody><SkeletonRows rows={3} /></CardBody>
        ) : data.custom.length === 0 ? (
          <CardBody>
            <EmptyState size="sm" title="No custom roles yet" hint="Create one when a built-in role gives too much or too little."
              action={writable ? <Button size="sm" onClick={openNew}><Plus /> New role</Button> : undefined} />
          </CardBody>
        ) : (
          <Table>
            <THead><TR><TH>Role</TH><TH className="hidden sm:table-cell">Features</TH><TH className="hidden sm:table-cell">Staff</TH><TH className="w-0" /></TR></THead>
            <TBody>
              {data.custom.map((r) => (
                <TR key={r.id}>
                  <TD>
                    <div className="font-medium">{r.name}</div>
                    {r.description && <div className="text-xs text-muted-foreground">{r.description}</div>}
                  </TD>
                  <TD className="hidden sm:table-cell">{count(r.permissions)}</TD>
                  <TD className="hidden sm:table-cell">{r.members}</TD>
                  <TD>
                    <RowActions subject={r.name} items={[
                      { label: "View permissions", icon: <Eye />, onSelect: () => setViewing({ title: r.name, perms: r.permissions }) },
                      ...(writable ? [
                        { label: "Edit", icon: <Pencil />, onSelect: () => openEdit(r) },
                        { label: "Remove…", icon: <Trash2 />, danger: true, onSelect: () => { setActionErr(null); setDeleting(r); } },
                      ] : []),
                    ]} />
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <Card>
        <CardHeader><CardTitle>Built-in roles</CardTitle></CardHeader>
        <Table>
          <THead><TR><TH>Role</TH><TH className="hidden sm:table-cell">Features</TH><TH className="w-0" /></TR></THead>
          <TBody>
            {(data?.built_in ?? []).map((b) => (
              <TR key={b.key}>
                <TD className="font-medium">{ROLE_LABELS[b.key as SiteRole] ?? b.key}</TD>
                <TD className="hidden sm:table-cell">{b.all ? "Everything" : count(b.permissions)}</TD>
                <TD>
                  <Button size="sm" variant="ghost" onClick={() => setViewing({ title: ROLE_LABELS[b.key as SiteRole] ?? b.key, perms: b.permissions, all: b.all })}>
                    <Eye /> View
                  </Button>
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </Card>

      <Sheet open={draft !== null} onOpenChange={(v) => !v && !busy && setDraft(null)}>
        <SheetContent width="lg">
          {draft && data && (
            <>
              <SheetHeader icon={<ShieldCheck />} eyebrow="Role" title={draft.id ? `Edit ${draft.name || "role"}` : "New role"}
                badges={<Badge tone="neutral">{chosen} feature{chosen === 1 ? "" : "s"}</Badge>} />
              <SheetBody>
                <ErrorBanner err={actionErr} />
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field label="Name" required>
                    <Input value={draft.name} maxLength={60} onChange={(e) => setDraft({ ...draft, name: e.target.value })} placeholder="e.g. Night desk" />
                  </Field>
                  <Field label="Description">
                    <Input value={draft.description} maxLength={300} onChange={(e) => setDraft({ ...draft, description: e.target.value })} placeholder="What the job is" />
                  </Field>
                </div>
                {groups.map(([group, features]) => (
                  <SheetSection key={group} title={group}
                    actions={
                      <div className="flex gap-1">
                        <Button size="xs" variant="ghost" onClick={() => setGroup(features, "none")}>None</Button>
                        <Button size="xs" variant="ghost" onClick={() => setGroup(features, "read")}>View all</Button>
                      </div>
                    }>
                    <ul className="divide-y divide-border">
                      {features.map((f) => {
                        const max = (data.max[f.key] ?? "none") as Level;
                        const options = (["none", "read", "write"] as Level[])
                          .filter((l) => l === "none" || (l === "read" && max !== "none") || (l === "write" && max === "write"))
                          .map((l) => ({ value: l, label: LEVEL_LABEL[l] }));
                        return (
                          <li key={f.key} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1.5 py-2.5">
                            <div className="min-w-0 flex-1">
                              <div className="flex flex-wrap items-center gap-1.5 text-sm font-medium">
                                {f.label}
                                {f.sensitive && <Badge tone="warn" size="sm">{f.sensitive}</Badge>}
                              </div>
                              <div className="text-xs text-muted-foreground">
                                {f.help}{max !== "write" && " Changing it stays with Site admin."}
                              </div>
                            </div>
                            <Segmented size="sm" label={f.label} value={(draft.perms[f.key] ?? "none") as Level}
                              onChange={(v) => setLevel(f.key, v)} options={options} />
                          </li>
                        );
                      })}
                    </ul>
                  </SheetSection>
                ))}
              </SheetBody>
              <SheetFooter>
                <Button variant="ghost" onClick={() => setDraft(null)} disabled={busy}>Cancel</Button>
                <Button onClick={() => { setActionErr(null); setConfirming(true); }}
                  disabled={busy || draft.name.trim().length < 2 || chosen === 0}
                  title={draft.name.trim().length < 2 ? "Give the role a name" : chosen === 0 ? "Choose at least one feature" : undefined}>
                  {draft.id ? "Save role" : "Create role"}
                </Button>
              </SheetFooter>
            </>
          )}
        </SheetContent>
      </Sheet>

      <Sheet open={viewing !== null} onOpenChange={(v) => !v && setViewing(null)}>
        <SheetContent width="md">
          {viewing && (
            <>
              <SheetHeader icon={<Eye />} eyebrow="Permissions" title={viewing.title} />
              <SheetBody>
                {viewing.all ? (
                  <p className="text-sm">Site admin can see and change everything, including staff accounts and roles.</p>
                ) : groups.map(([group, features]) => {
                  const held = features.filter((f) => viewing.perms[f.key] === "read" || viewing.perms[f.key] === "write");
                  if (held.length === 0) return null;
                  return (
                    <SheetSection key={group} title={group}>
                      <ul className="divide-y divide-border">
                        {held.map((f) => (
                          <li key={f.key} className="flex items-center justify-between gap-3 py-2 text-sm">
                            <span>{f.label}</span>
                            <Badge tone={viewing.perms[f.key] === "write" ? "accent" : "neutral"} size="sm">{LEVEL_LABEL[viewing.perms[f.key] as Level]}</Badge>
                          </li>
                        ))}
                      </ul>
                    </SheetSection>
                  );
                })}
              </SheetBody>
            </>
          )}
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={confirming}
        onOpenChange={(v) => !v && !busy && setConfirming(false)}
        title={draft?.id ? "Save this role?" : "Create this role?"}
        description={draft?.id ? "Everyone who holds it gets the new permissions from their next action." : "Nobody holds it until you give it to staff."}
        confirmLabel={draft?.id ? "Save role" : "Create role"}
        busy={busy}
        error={actionErr}
        requirePassword strictPassword
        onConfirm={save}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && !busy && setDeleting(null)}
        title={deleting ? `Remove ${deleting.name}?` : "Remove this role?"}
        description={deleting && deleting.members > 0
          ? `${deleting.members} staff account${deleting.members === 1 ? "" : "s"} still hold it. Take it away from them in Operators first.`
          : "Nobody holds it. It is removed for good."}
        confirmLabel="Remove role"
        confirmVariant="danger"
        busy={busy}
        error={actionErr}
        requirePassword strictPassword
        onConfirm={remove}
      />
    </PageShell>
  );
}
