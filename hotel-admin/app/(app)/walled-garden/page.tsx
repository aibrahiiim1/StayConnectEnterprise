"use client";

import { useEffect, useMemo, useState } from "react";
import { api, ApiError, ListResp, WalledGardenRule, WalledGardenPolicy } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card } from "@/components/ui/card";
import { RowActions } from "@/components/ui/row-actions";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { DescriptionList } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { AlertTriangle, Globe, Pencil, Plus, RefreshCw } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { cn, formatDate, formatRelative } from "@/lib/utils";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";
import { useOperatorRoles } from "@/lib/whoami-context";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const KIND_LABELS: Record<string, string> = {
  domain: "Domain name",
  ip: "Single address",
  cidr: "Address range",
};

const PLACEHOLDERS: Record<string, string> = { domain: "checkout.example.com", ip: "203.0.113.9", cidr: "203.0.113.0/28" };

const SERVER_WORDS: Record<string, string> = {
  "the walled-garden policy could not be read":
    "The address range limit could not be read, so rules cannot be saved right now. Nothing has been changed.",
  "kind must be domain|ip|cidr with a valid value": "The address does not match the type chosen.",
  "ports must be 1..65535": "Ports must be whole numbers between 1 and 65535.",
  "rule not found": "This rule no longer exists. It may have been removed by someone else.",
};

function sentence(text: string): string {
  const t = text.trim();
  if (!t) return t;
  const s = t.charAt(0).toUpperCase() + t.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}

function plainError(e: unknown): unknown {
  if (e instanceof ApiError) return new Error(SERVER_WORDS[e.message] ?? sentence(e.message));
  return e;
}

function ipv4(text: string): number[] | null {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(text);
  if (!m) return null;
  const parts = m.slice(1).map(Number);
  return parts.every((p) => p <= 255) ? parts : null;
}

function notPublic(o: number[]): boolean {
  const [a, b] = o;
  return (
    a === 0 || a === 10 || a === 127 || a >= 224 ||
    (a === 100 && b >= 64 && b <= 127) ||
    (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 168)
  );
}

function addressProblem(kind: string, raw: string, widest: number | null): string | null {
  const v = raw.trim();
  if (!v) return "Enter an address.";
  if (kind === "domain") {
    if (v.includes("*")) return "Wildcards are not supported. Enter one host name, for example checkout.stripe.com.";
    if (/[\s/:]/.test(v) || !v.includes(".")) return "Enter a host name only, for example checkout.stripe.com, without https:// or a path.";
    return null;
  }
  if (kind === "ip") {
    const o = ipv4(v);
    if (!o) return "Enter one IPv4 address, for example 203.0.113.9.";
    if (notPublic(o)) return "Private, link-local and reserved addresses are never allowed before sign-in.";
    return null;
  }
  const [addr, len, extra] = v.split("/");
  const o = ipv4(addr ?? "");
  const prefix = Number(len);
  if (!o || extra !== undefined || len === undefined || !/^\d{1,2}$/.test(len) || prefix > 32) {
    return "Enter a range as an address and a prefix, for example 203.0.113.0/28.";
  }
  const floor = Math.max(widest ?? 8, 8);
  if (prefix < floor) return `/${prefix} is wider than this site allows. Use /${floor} or narrower.`;
  const n = ((o[0] << 24) >>> 0) + (o[1] << 16) + (o[2] << 8) + o[3];
  const mask = prefix === 0 ? 0 : (0xffffffff << (32 - prefix)) >>> 0;
  const start = (n & mask) >>> 0;
  if (start !== n) {
    const s = [start >>> 24, (start >>> 16) & 255, (start >>> 8) & 255, start & 255].join(".");
    return `This is not the start of the range. Write it as ${s}/${prefix}.`;
  }
  if (notPublic(o)) return "Private, link-local and reserved addresses are never allowed before sign-in.";
  return null;
}

function portsProblem(raw: string): string | null {
  const parts = raw.split(",").map((p) => p.trim()).filter(Boolean);
  if (parts.some((p) => !/^\d+$/.test(p) || Number(p) < 1 || Number(p) > 65535)) {
    return "Ports must be whole numbers between 1 and 65535, separated by commas.";
  }
  return null;
}

function portsText(r: WalledGardenRule): string {
  return r.ports && r.ports.length ? r.ports.join(", ") : "All ports";
}

type Draft = { kind: string; value: string; ports: string; description: string };
const EMPTY: Draft = { kind: "domain", value: "", ports: "", description: "" };

export default function WalledGardenPage() {
  const toast = useToast();
  const caps = useCapabilities();
  const reasons: string[] = [
    "a captive-portal check, so the device notices the sign-in page",
    ...(moduleLicensed(caps, "card_payment") ? ["a payment provider, so a paid package's payment page loads"] : []),
    ...(moduleLicensed(caps, "social_login") ? ["an identity provider, for social login"] : []),
  ];
  const [rows, setRows] = useState<WalledGardenRule[] | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [policy, setPolicy] = useState<WalledGardenPolicy | null>(null);
  const [policyFailed, setPolicyFailed] = useState(false);
  const [editingPolicy, setEditingPolicy] = useState(false);
  const [policyPrefix, setPolicyPrefix] = useState("24");
  const [policyFieldErr, setPolicyFieldErr] = useState<string | null>(null);
  const [policyErr, setPolicyErr] = useState<unknown>(null);
  const [editing, setEditing] = useState<WalledGardenRule | null>(null);
  const [viewing, setViewing] = useState<WalledGardenRule | null>(null);
  const roles = useOperatorRoles();
  const [err, setErr] = useState<unknown>(null);
  const [showNew, setShowNew] = useState(false);
  const [deleting, setDeleting] = useState<WalledGardenRule | null>(null);
  const [deleteErr, setDeleteErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);
  const [draft, setDraft] = useState<Draft>(EMPTY);
  const [initial, setInitial] = useState<Draft>(EMPTY);
  const [attempted, setAttempted] = useState(false);

  const writable = roles !== null && canWrite("walled-garden", roles);
  const dirty = useMemo(() => JSON.stringify(draft) !== JSON.stringify(initial), [draft, initial]);
  const widest = policy?.widest_prefix_len ?? null;
  const valueErr = attempted ? addressProblem(draft.kind, draft.value, widest) : null;
  const portErr = attempted ? portsProblem(draft.ports) : null;

  async function load() {
    setLoading(true);
    try {
      setRows((await api.get<ListResp<WalledGardenRule>>("/walled-garden")).data);
      setLoadFailed(false);
      setErr(null);
    } catch (e) {
      setErr(plainError(e));
      setLoadFailed(true);
    }
    try {
      setPolicy(await api.get<WalledGardenPolicy>("/walled-garden/policy"));
      setPolicyFailed(false);
    } catch {
      setPolicyFailed(true);
    }
    setLoading(false);
  }
  useEffect(() => {
    void load();
  }, []);

  function set<K extends keyof Draft>(k: K, v: Draft[K]) {
    setDraft((d) => ({ ...d, [k]: v }));
  }

  function openForm(r: WalledGardenRule | null) {
    const d: Draft = r
      ? { kind: r.kind, value: r.value, ports: (r.ports ?? []).join(", "), description: r.description ?? "" }
      : EMPTY;
    setEditing(r);
    setViewing(null);
    setDraft(d);
    setInitial(d);
    setAttempted(false);
    setFormErr(null);
    setShowNew(true);
  }

  function changeKind(kind: string) {
    setDraft((d) => ({ ...d, kind, value: d.kind === kind ? d.value : "" }));
  }

  async function onSavePolicy({ reason, password }: { reason: string; password: string }) {
    const n = Number(policyPrefix);
    const min = policy?.min ?? 16;
    const max = policy?.max ?? 32;
    if (!/^\d+$/.test(policyPrefix.trim()) || n < min || n > max) {
      setPolicyFieldErr(`Enter a whole number between ${min} and ${max}.`);
      return;
    }
    setBusy(true);
    setPolicyErr(null);
    try {
      setPolicy(await api.put<WalledGardenPolicy>("/walled-garden/policy", { widest_prefix_len: n, reason, password }));
      setEditingPolicy(false);
      toast.success("Limit saved", "It applies to rules added or edited from now on; existing rules are not changed.");
      await load();
    } catch (e) {
      setPolicyErr(plainError(e));
    } finally {
      setBusy(false);
    }
  }

  async function onSave() {
    setAttempted(true);
    if (addressProblem(draft.kind, draft.value, widest) || portsProblem(draft.ports)) return;
    setBusy(true);
    setFormErr(null);
    const parsed = draft.ports.split(",").map((p) => p.trim()).filter(Boolean).map(Number);
    try {
      const body = {
        kind: draft.kind,
        value: draft.value.trim(),
        ports: parsed.length ? parsed : undefined,
        description: draft.description.trim() || undefined,
      };
      if (editing) await api.put(`/walled-garden/${editing.id}`, body);
      else await api.post("/walled-garden", body);
      setShowNew(false);
      toast.success(
        editing ? "Rule saved" : "Rule added",
        draft.kind === "domain"
          ? "Being applied now. A domain opens at the addresses its name resolves to; if it does not resolve, nothing opens."
          : "Being applied now for devices that have not signed in.",
      );
      await load();
    } catch (e) {
      setFormErr(plainError(e));
    } finally {
      setBusy(false);
    }
  }

  async function onDelete() {
    if (!deleting) return;
    setBusy(true);
    setDeleteErr(null);
    try {
      await api.del(`/walled-garden/${deleting.id}`);
      setDeleting(null);
      toast.success("Rule removed");
      await load();
    } catch (e) {
      setDeleteErr(plainError(e));
    } finally {
      setBusy(false);
    }
  }

  const tryAgain = (
    <Button variant="secondary" disabled={loading} onClick={() => void load()}>
      <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
    </Button>
  );

  return (
    <PageShell>
      <PageHeader
        title="Allowed sites"
        description="Addresses reachable before sign-in. Keep the list to what the sign-in page needs."
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
        actions={writable && <Button onClick={() => openForm(null)}><Plus /> Add rule</Button>}
      />

      {policy && (
        <div className={cn("flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-card px-4 py-3 shadow-card", FADE)}>
          <div className="min-w-0 space-y-0.5 text-sm text-muted-foreground">
            <p>
              Widest address range allowed: <strong className="font-mono text-foreground">/{policy.widest_prefix_len}</strong>{" "}
              ({(2 ** (32 - policy.widest_prefix_len)).toLocaleString()} addresses). Private, link-local and reserved
              addresses are never allowed before sign-in.
            </p>
            {policy.updated_at && (
              <p className="text-caption">
                Last changed {formatRelative(policy.updated_at)}{policy.updated_by ? ` by ${policy.updated_by}` : ""}.
              </p>
            )}
          </div>
          {writable && (
            <Button size="sm" variant="secondary" onClick={() => { setPolicyPrefix(String(policy.widest_prefix_len)); setPolicyFieldErr(null); setPolicyErr(null); setEditingPolicy(true); }}>
              Change limit
            </Button>
          )}
        </div>
      )}
      {!policy && policyFailed && (
        <div className={FADE}>
          <Callout tone="warning" title="The address range limit could not be read">
            <p>Rules cannot be added or changed until it can, because each rule is checked against it. Nothing has been changed.</p>
            <div className="mt-2">{tryAgain}</div>
          </Callout>
        </div>
      )}
      {roles !== null && !writable && <ReadOnlyNotice>Your role can see which sites are allowed before sign-in but not change them.</ReadOnlyNotice>}
      {rows !== null && <ErrorBanner err={err} className="mb-0" />}

      <Card className="overflow-hidden">
        {rows === null ? (
          loadFailed ? (
            <div className={FADE}>
              <EmptyState
                icon={<AlertTriangle />}
                title="Allowed sites could not be loaded"
                hint="The appliance did not return its list of allowed sites. Nothing has been changed."
                action={tryAgain}
              />
            </div>
          ) : (
            <SkeletonRows rows={4} cols={5} />
          )
        ) : rows.length === 0 ? (
          <div className={FADE}>
            <EmptyState
              size="sm"
              icon={<Globe />}
              title="No rules added"
              hint="Before sign-in, devices can already reach the sign-in page, DNS and the pages of any payment or identity provider that is switched on. Add a rule only for something else the sign-in page needs."
              action={writable ? <Button onClick={() => openForm(null)}><Plus /> Add the first rule</Button> : undefined}
            />
          </div>
        ) : (
          <div className={FADE}>
            <Table label="Allowed sites">
              <THead>
                <TR><TH>Type</TH><TH>Address</TH><TH>Ports</TH><TH className="hidden md:table-cell">Why</TH><TH className="hidden lg:table-cell">Added</TH><TH><span className="sr-only">Actions</span></TH></TR>
              </THead>
              <TBody>
                {rows.map((r) => (
                  <TR key={r.id} onClick={() => (writable ? openForm(r) : setViewing(r))}>
                    <TD nowrap><Badge>{KIND_LABELS[r.kind] ?? r.kind}</Badge></TD>
                    <TD className="font-mono text-sm">
                      <span className="break-all">{r.value}</span>
                      {r.wider_than_policy && (
                        <div className="mt-1"><Badge tone="warn">Wider than this site allows. Edit or remove it.</Badge></div>
                      )}
                    </TD>
                    <TD nowrap muted className="font-mono text-xs">{portsText(r)}</TD>
                    <TD truncate muted title={r.description || undefined} className="hidden md:table-cell">{r.description || "Not given"}</TD>
                    <TD nowrap muted className="hidden lg:table-cell">{formatRelative(r.created_at)}</TD>
                    <TD className="w-0">
                      {writable ? (
                        <RowActions
                          subject={r.value}
                          primary={{ label: "Edit", onSelect: () => openForm(r) }}
                          items={[
                            { label: "Details", onSelect: () => setViewing(r) },
                            { label: "Remove…", danger: true, onSelect: () => { setDeleteErr(null); setDeleting(r); } },
                          ]}
                        />
                      ) : (
                        <RowActions subject={r.value} primary={{ label: "Details", onSelect: () => setViewing(r) }} />
                      )}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </div>
        )}
      </Card>

      <DetailsDrawer
        open={viewing !== null}
        onOpenChange={(v) => !v && setViewing(null)}
        eyebrow="Allowed site"
        title={viewing?.value ?? ""}
        badges={viewing ? <Badge>{KIND_LABELS[viewing.kind] ?? viewing.kind}</Badge> : undefined}
        actions={writable && viewing ? <Button onClick={() => openForm(viewing)}><Pencil /> Edit</Button> : undefined}
      >
        {viewing && (
          <>
            {viewing.wider_than_policy && (
              <Callout tone="warning">This range is wider than this site now allows. Edit or remove it.</Callout>
            )}
            <DetailsSection title="Rule">
              <DescriptionList items={[
                { label: "Type", value: KIND_LABELS[viewing.kind] ?? viewing.kind },
                { label: "Address", value: <span className="break-all font-mono">{viewing.value}</span> },
                { label: "Ports", value: <span className="font-mono">{portsText(viewing)}</span> },
                { label: "Why it is needed", value: viewing.description || "Not given" },
                { label: "Added", value: formatDate(viewing.created_at) },
              ]} />
            </DetailsSection>
            {viewing.kind === "domain" && (
              <p className="text-caption text-muted-foreground">
                Opened at the addresses this name resolves to. Subdomains are not included.
              </p>
            )}
          </>
        )}
      </DetailsDrawer>

      <DialogForm
        open={showNew}
        onOpenChange={(v) => !v && setShowNew(false)}
        title={editing ? "Edit this allowed site" : "Allow a site before sign-in"}
        description="Devices that have not signed in will be able to reach this address."
        submitLabel={editing ? "Save rule" : "Add rule"}
        busy={busy}
        error={formErr}
        dirty={dirty}
        disabled={!draft.value.trim()}
        disabledReason="Enter an address to save."
        onSubmit={onSave}
      >
        <Field
          label="Type"
          hint={
            draft.kind === "domain" ? "One host name. Subdomains are not included."
              : draft.kind === "cidr" ? "A range of public addresses in CIDR notation."
                : "One public IPv4 address."
          }
        >
          <Select value={draft.kind} onChange={(e) => changeKind(e.target.value)}>
            <option value="domain">Domain name</option>
            <option value="ip">Single address</option>
            <option value="cidr">Address range</option>
          </Select>
        </Field>
        <Field
          label="Address"
          required
          error={valueErr ? <span key={valueErr} className={FADE}>{valueErr}</span> : undefined}
          hint={
            draft.kind === "domain" ? "For example: checkout.stripe.com"
              : draft.kind === "cidr" ? `For example: 203.0.113.0/28${widest ? `. /${widest} is the widest allowed.` : ""}`
                : "For example: 203.0.113.9"
          }
        >
          <Input
            value={draft.value}
            onChange={(e) => set("value", e.target.value)}
            required
            autoCapitalize="none"
            spellCheck={false}
            placeholder={PLACEHOLDERS[draft.kind]}
          />
        </Field>
        <Field
          label="Ports"
          hint="Comma separated. Leave empty to allow every port."
          error={portErr ? <span key={portErr} className={FADE}>{portErr}</span> : undefined}
        >
          <Input value={draft.ports} onChange={(e) => set("ports", e.target.value)} placeholder="80, 443" />
        </Field>
        <Field label="Why it is needed" hint="Written down so the next operator does not have to guess.">
          <Input
            value={draft.description}
            onChange={(e) => set("description", e.target.value)}
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
        <Field
          label="Widest range (prefix length)"
          hint={policy ? `Between /${policy.min} and /${policy.max}. Default /${policy.default}.` : undefined}
          error={policyFieldErr ? <span key={policyFieldErr} className={FADE}>{policyFieldErr}</span> : undefined}
        >
          <Input
            type="number"
            min={policy?.min ?? 16}
            max={policy?.max ?? 32}
            value={policyPrefix}
            onChange={(e) => { setPolicyPrefix(e.target.value); setPolicyFieldErr(null); }}
          />
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
        error={deleteErr}
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
