"use client";

// ALLOWED SITES — what a guest's device may reach before it has signed in.
//
// Two changes beyond the visual refit:
//
//   * the form is a DIALOG. It was a card pushed above the table, which on a long rule list opened off-screen.
//   * deleting a rule no longer goes through `window.confirm`. A browser confirm cannot say which rule is about
//     to be removed or what removing it does, and "Delete this walled-garden rule?" was the whole of its
//     explanation — for an action that can stop a captive portal or a payment page from loading.

import { useEffect, useState } from "react";
import { api, ListResp, Whoami, WalledGardenRule, WalledGardenPolicy } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { SkeletonRows } from "@/components/ui/misc";
import { Plus, Shield, Globe } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { formatRelative, errMsg } from "@/lib/utils";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";

function kindTone(kind: string): "info" | "default" | "warn" {
  switch (kind) {
    case "domain": return "info";
    case "cidr":   return "warn";
    default:       return "default";
  }
}

const KIND_LABELS: Record<string, string> = {
  domain: "Domain name",
  ip: "Single address",
  cidr: "Address range",
};

export default function WalledGardenPage() {
  const toast = useToast();
  // WHY A SITE MIGHT BE ALLOWED, limited to what this site can have: a payment provider only with Card payment
  // licensed, an identity provider only with Social sign-in. Unknown module state lists neither (fail closed).
  const caps = useCapabilities();
  const reasons: string[] = [
    "a captive-portal check, so the device notices the sign-in page",
    ...(moduleLicensed(caps, "card_payment") ? ["a payment provider, so a paid package's payment page loads"] : []),
    ...(moduleLicensed(caps, "social_login") ? ["an identity provider, for social login"] : []),
  ];
  const [rows, setRows] = useState<WalledGardenRule[] | null>(null);
  // The widest network a rule may name: a site setting (0125), changed with a reason and the password.
  const [policy, setPolicy] = useState<WalledGardenPolicy | null>(null);
  const [editingPolicy, setEditingPolicy] = useState(false);
  const [policyPrefix, setPolicyPrefix] = useState("24");
  const [policyErr, setPolicyErr] = useState<unknown>(null);
  // null = adding a new rule; a rule = editing that one
  const [editing, setEditing] = useState<WalledGardenRule | null>(null);
  const [roles, setRoles] = useState<string[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [showNew, setShowNew] = useState(false);
  const [deleting, setDeleting] = useState<WalledGardenRule | null>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);

  // The form fields are held in state rather than read out of FormData on submit. The submit button lives in the
  // dialog footer, outside the <form>, so it reaches the form by id — and a controlled form is what lets the
  // footer's button be disabled until the value is actually present.
  const [kind, setKind] = useState("domain");
  const [value, setValue] = useState("");
  const [ports, setPorts] = useState("");
  const [description, setDescription] = useState("");

  const writable = roles !== null && canWrite("walled-garden", roles);

  async function load() {
    try { setRows((await api.get<ListResp<WalledGardenRule>>("/walled-garden")).data); }
    catch (e) { setErr(e); }
    try { setPolicy(await api.get<WalledGardenPolicy>("/walled-garden/policy")); }
    catch { /* the list still works; the limit line is simply not shown */ }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);

  function openNew() {
    setEditing(null);
    setKind("domain"); setValue(""); setPorts(""); setDescription("");
    setFormErr(null);
    setShowNew(true);
  }

  function openEdit(r: WalledGardenRule) {
    setEditing(r);
    setKind(r.kind); setValue(r.value); setPorts((r.ports ?? []).join(", ")); setDescription(r.description ?? "");
    setFormErr(null);
    setShowNew(true);
  }

  async function onSavePolicy({ reason, password }: { reason: string; password: string }) {
    setBusy(true); setPolicyErr(null);
    try {
      setPolicy(await api.put<WalledGardenPolicy>("/walled-garden/policy", {
        widest_prefix_len: Number(policyPrefix), reason, password,
      }));
      setEditingPolicy(false);
      toast.success("Limit saved", "It applies to rules added or edited from now on; existing rules are not changed.");
      await load();
    } catch (e) { setPolicyErr(e); }
    finally { setBusy(false); }
  }

  async function onCreate() {
    setBusy(true); setFormErr(null);
    const parsed = ports.split(",").map((p) => p.trim()).filter(Boolean).map(Number);
    if (parsed.some((p) => !Number.isInteger(p) || p < 1 || p > 65535)) {
      setFormErr("Ports must be whole numbers between 1 and 65535."); setBusy(false); return;
    }
    try {
      const body = {
        kind,
        value: value.trim(),
        ports: parsed.length ? parsed : undefined,
        description: description.trim() || undefined,
      };
      if (editing) await api.put(`/walled-garden/${editing.id}`, body);
      else await api.post("/walled-garden", body);
      setShowNew(false);
      toast.success(editing ? "Rule saved" : "Rule added", "Devices that have not signed in can reach it now.");
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onDelete() {
    if (!deleting) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/walled-garden/${deleting.id}`);
      setDeleting(null);
      toast.success("Rule removed");
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  return (
    <PageShell>
      <PageHeader
        icon={<Globe />}
        eyebrow="Client access"
        title="Allowed sites"
        description="Addresses reachable before sign-in, without any authentication — keep the list to what the sign-in page needs."
        help={
          <>
            <HelpSection title="What an allowed site is">
              <p>
                An address a client&apos;s device may reach before it has signed in. Everything listed here is
                reachable without any authentication at all, so keep it to what the sign-in page itself needs.
              </p>
              <HelpList
                items={reasons.map((r, i) => <>{r}{i === reasons.length - 1 ? "." : ";"}</>)}
              />
            </HelpSection>
            <HelpSection title="Writing a rule">
              <HelpList
                items={[
                  <>A <strong>domain</strong> is one host name, opened at the addresses it resolves to (checkout.stripe.com, not *.stripe.com); an <strong>address range</strong> uses CIDR notation, for example 10.0.0.0/24.</>,
                  <>Leave <strong>Ports</strong> empty to allow every port.</>,
                  <>Say <strong>why it is needed</strong>, so the next operator does not have to guess before removing it.</>,
                ]}
              />
            </HelpSection>
          </>
        }
        actions={writable && <Button onClick={openNew}><Plus /> Add rule</Button>}
      />

      {policy && (
        <p className="text-sm text-muted-foreground">
          Widest address range allowed: <strong className="font-mono">/{policy.widest_prefix_len}</strong>{" "}
          ({(2 ** (32 - policy.widest_prefix_len)).toLocaleString()} addresses). Private, link-local and reserved
          addresses are never allowed before sign-in.{" "}
          {writable && (
            <Button size="sm" variant="ghost" onClick={() => { setPolicyPrefix(String(policy.widest_prefix_len)); setPolicyErr(null); setEditingPolicy(true); }}>
              Change limit
            </Button>
          )}
        </p>
      )}
      {roles !== null && !writable && <ReadOnlyNotice>Your role can see which sites are allowed before sign-in but not change them.</ReadOnlyNotice>}
      <ErrorBanner err={err} className="mb-0" />

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={4} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<Shield />}
              title="Nothing is allowed before sign-in"
              hint="Add a rule to let captive-portal detection or a payment page load for a client who has not signed in yet."
              action={writable ? <Button onClick={openNew}><Plus /> Add the first rule</Button> : undefined}
            />
          ) : (
            <Table>
              <THead>
                <TR><TH>Type</TH><TH>Address</TH><TH>Ports</TH><TH>Why</TH><TH>Added</TH><TH /></TR>
              </THead>
              <TBody>
                {rows.map((r) => (
                  <TR key={r.id}>
                    <TD><Badge tone={kindTone(r.kind)}>{KIND_LABELS[r.kind] ?? r.kind}</Badge></TD>
                    <TD className="font-mono text-sm">
                      {r.value}
                      {r.wider_than_policy && (
                        <Badge tone="warn" className="ms-2">Wider than this site allows — edit or remove it</Badge>
                      )}
                    </TD>
                    <TD className="font-mono text-xs text-muted-foreground">
                      {r.ports && r.ports.length ? r.ports.join(", ") : "all"}
                    </TD>
                    <TD className="text-sm text-muted-foreground">{r.description || "—"}</TD>
                    <TD className="text-sm text-muted-foreground">{formatRelative(r.created_at)}</TD>
                    <TD className="text-right">
                      {writable && (
                        <>
                          <Button size="sm" variant="ghost" onClick={() => openEdit(r)}>Edit</Button>
                          <Button size="sm" variant="ghost" onClick={() => { setFormErr(null); setDeleting(r); }}>
                            Remove
                          </Button>
                        </>
                      )}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </CardBody>
      </Card>

      <DialogForm
        open={showNew}
        onOpenChange={(v) => !v && setShowNew(false)}
        title={editing ? "Edit this allowed site" : "Allow a site before sign-in"}
        description="Devices that have not signed in will be able to reach this address."
        submitLabel={editing ? "Save rule" : "Add rule"}
        busy={busy}
        error={formErr}
        disabled={!value.trim()}
        onSubmit={onCreate}
      >
        <Field label="Type" hint="A domain covers its subdomains; a range uses CIDR notation.">
          <Select value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="domain">Domain name</option>
            <option value="ip">Single address</option>
            <option value="cidr">Address range</option>
          </Select>
        </Field>
        <Field
          label="Address"
          required
          hint={
            kind === "domain" ? "For example: checkout.stripe.com"
              : kind === "cidr" ? "For example: 10.0.0.0/24"
                : "For example: 203.0.113.9 (a public address: private and reserved ones are refused)"
          }
        >
          <Input
            value={value}
            onChange={(e) => setValue(e.target.value)}
            required
            placeholder={kind === "domain" ? "example.com" : kind === "cidr" ? "10.0.0.0/24" : "1.2.3.4"}
          />
        </Field>
        <Field label="Ports" hint="Comma separated. Leave empty to allow every port.">
          <Input value={ports} onChange={(e) => setPorts(e.target.value)} placeholder="80, 443" />
        </Field>
        <Field label="Why it is needed" hint="Written down so the next operator does not have to guess.">
          <Input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Stripe checkout for paid packages"
          />
        </Field>
      </DialogForm>

      <ConfirmDialog
        open={editingPolicy}
        onOpenChange={(v) => !v && setEditingPolicy(false)}
        title="Change the widest address range allowed?"
        description="A wider range lets clients that have not signed in reach more of the internet. It applies to rules added or edited from now on; existing rules are not changed."
        confirmLabel="Save limit"
        busy={busy}
        error={policyErr}
        requireReason
        reasonMinLength={4}
        requirePassword
        onConfirm={onSavePolicy}
      >
        <Field label="Widest range (prefix length)" hint={policy ? `Between /${policy.min} and /${policy.max}. Default /${policy.default}.` : undefined}>
          <Input type="number" min={policy?.min ?? 16} max={policy?.max ?? 32} value={policyPrefix} onChange={(e) => setPolicyPrefix(e.target.value)} />
        </Field>
      </ConfirmDialog>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="Remove this allowed site?"
        description={
          deleting
            ? `Devices that have not signed in will no longer be able to reach ${deleting.value}. If the sign-in page depends on it, clients may be unable to get online at all.`
            : undefined
        }
        confirmLabel="Remove"
        confirmVariant="danger"
        busy={busy}
        error={formErr}
        onConfirm={onDelete}
      >
        {deleting?.description && (
          <p className="rounded-md border border-border bg-surface/60 p-3 text-xs text-muted-foreground">
            Recorded reason: {deleting.description}
          </p>
        )}
      </ConfirmDialog>
    </PageShell>
  );
}
