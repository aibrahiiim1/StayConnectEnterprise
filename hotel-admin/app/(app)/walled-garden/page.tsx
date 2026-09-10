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
import { api, ListResp, Whoami, WalledGardenRule } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { SkeletonRows } from "@/components/ui/misc";
import { Plus, Shield } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { formatRelative, errMsg } from "@/lib/utils";

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
  const [rows, setRows] = useState<WalledGardenRule[] | null>(null);
  const [roles, setRoles] = useState<string[]>([]);
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

  const writable = canWrite("walled-garden", roles);

  async function load() {
    try { setRows((await api.get<ListResp<WalledGardenRule>>("/walled-garden")).data); }
    catch (e) { setErr(e); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);

  function openNew() {
    setKind("domain"); setValue(""); setPorts(""); setDescription("");
    setFormErr(null);
    setShowNew(true);
  }

  async function onCreate() {
    setBusy(true); setFormErr(null);
    const parsed = ports.split(",").map((p) => p.trim()).filter(Boolean).map(Number);
    if (parsed.some((p) => !Number.isInteger(p) || p < 1 || p > 65535)) {
      setFormErr("Ports must be whole numbers between 1 and 65535."); setBusy(false); return;
    }
    try {
      await api.post("/walled-garden", {
        kind,
        value: value.trim(),
        ports: parsed.length ? parsed : undefined,
        description: description.trim() || undefined,
      });
      setShowNew(false);
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
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  return (
    <PageShell>
      <PageHeader
        eyebrow="Guest portal"
        title="Allowed sites"
        description="Addresses a guest's device may reach before it has signed in. Keep it to what the sign-in page itself needs — a captive-portal check, a payment provider, an identity provider — because everything listed here is reachable without any authentication at all."
        actions={writable && <Button onClick={openNew}><Plus /> Add rule</Button>}
      />

      <ErrorBanner err={err} />

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={4} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<Shield />}
              title="Nothing is allowed before sign-in"
              hint="Add a rule to let captive-portal detection or a payment page load for a guest who has not signed in yet."
              action={writable ? <Button onClick={openNew}><Plus /> Add the first rule</Button> : undefined}
            />
          ) : (
            <Table>
              <THead>
                <TR><TH>Type</TH><TH>Address</TH><TH>Ports</TH><TH>Why</TH><TH>Added</TH><TH /></TR>
              </THead>
              <tbody>
                {rows.map((r) => (
                  <TR key={r.id}>
                    <TD><Badge tone={kindTone(r.kind)}>{KIND_LABELS[r.kind] ?? r.kind}</Badge></TD>
                    <TD className="font-mono text-sm">{r.value}</TD>
                    <TD className="font-mono text-xs text-muted-foreground">
                      {r.ports && r.ports.length ? r.ports.join(", ") : "all"}
                    </TD>
                    <TD className="text-sm text-muted-foreground">{r.description || "—"}</TD>
                    <TD className="text-sm text-muted-foreground">{formatRelative(r.created_at)}</TD>
                    <TD className="text-right">
                      {writable && (
                        <Button size="sm" variant="ghost" onClick={() => { setFormErr(null); setDeleting(r); }}>
                          Remove
                        </Button>
                      )}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      <DialogForm
        open={showNew}
        onOpenChange={(v) => !v && setShowNew(false)}
        title="Allow a site before sign-in"
        description="Devices that have not signed in will be able to reach this address."
        submitLabel="Add rule"
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
                : "For example: 203.0.113.9"
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
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="Remove this allowed site?"
        description={
          deleting
            ? `Devices that have not signed in will no longer be able to reach ${deleting.value}. If the sign-in page depends on it, guests may be unable to get online at all.`
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
