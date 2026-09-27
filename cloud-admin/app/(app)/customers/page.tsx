"use client";

import Link from "next/link";
import { Suspense, useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Building2, Plus } from "lucide-react";
import { api, itemsOf, qs, type Customer, type Items } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm } from "@/components/ui/dialog";
import { PageHeader, PageShell, Toolbar } from "@/components/ui/page";
import { SearchInput } from "@/components/ui/data";
import { Segmented } from "@/components/ui/tabs";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { StateBadge } from "@/components/status-badge";
import { usePermissions } from "@/lib/permissions";
import { useQueryState } from "@/lib/use-query-state";
import { recordStatusInfo } from "@/lib/status";

const FILTERS = ["status", "q"] as const;

export default function CustomersPage() {
  return (
    <Suspense fallback={null}>
      <Customers />
    </Suspense>
  );
}

function Customers() {
  const router = useRouter();
  const toast = useToast();
  const { can, subject } = usePermissions();
  const [f, setF] = useQueryState(FILTERS);
  const status = f.status || "active";
  const [rows, setRows] = useState<Customer[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [createErr, setCreateErr] = useState<unknown>(null);

  // A customer's own user has exactly one customer: go straight to it.
  const own = !can["customers.list"] ? subject.customerId : "";
  useEffect(() => {
    if (own) router.replace(`/customers/${own}`);
  }, [own, router]);

  const load = useCallback(async () => {
    try {
      const r = await api.get<Items<Customer>>(`/cloud/v1/customers${qs({ status, q: f.q })}`);
      setRows(itemsOf(r));
      setErr(null);
    } catch (e) {
      setErr(e);
      setRows([]);
    }
  }, [status, f.q]);

  useEffect(() => {
    if (!own) void load();
  }, [load, own]);

  async function create() {
    setBusy(true);
    setCreateErr(null);
    try {
      const c = await api.post<Customer>("/cloud/v1/customers", { name: name.trim() });
      toast.success("Customer created", c.name);
      setCreating(false);
      router.push(`/customers/${c.id}`);
    } catch (e) {
      setCreateErr(e);
    } finally {
      setBusy(false);
    }
  }

  if (own) return null;

  return (
    <PageShell>
      <PageHeader
        title="Customers"
        description="The organisations that own hotels. Open one to manage its sites, appliances, licenses and users."
        actions={
          can["customers.create"] ? (
            <Button onClick={() => { setName(""); setCreateErr(null); setCreating(true); }}>
              <Plus /> New customer
            </Button>
          ) : undefined
        }
      />

      <Card>
        <Toolbar className="border-b border-border px-4 py-3">
          <Segmented
            label="Show"
            value={status}
            onChange={(v) => setF({ status: v === "active" ? "" : v })}
            options={[
              { value: "active", label: "Active" },
              { value: "archived", label: "Archived" },
              { value: "all", label: "All" },
            ]}
          />
          <SearchInput value={f.q} onChange={(v) => setF({ q: v })} delay={300} placeholder="Search customers" label="Search customers" />
        </Toolbar>
        <ErrorBanner err={err} className="m-4" />
        {rows === null ? (
          <SkeletonRows rows={5} cols={5} />
        ) : rows.length === 0 ? (
          f.q || status !== "active" ? (
            <EmptyState title="No customers match" hint="Nothing matches this search." />
          ) : (
            <EmptyState
              icon={<Building2 />}
              title="No customers yet"
              hint="Create one here, or when you activate an appliance."
              action={can["customers.create"] ? <Button onClick={() => setCreating(true)}><Plus /> New customer</Button> : undefined}
            />
          )
        ) : (
          <Table aria-label="Customers">
            <THead>
              <TR>
                <TH>Customer</TH>
                <TH className="hidden sm:table-cell text-end">Sites</TH>
                <TH className="text-end">Appliances</TH>
                <TH className="hidden md:table-cell text-end">Active licenses</TH>
                <TH>Needs attention</TH>
              </TR>
            </THead>
            <tbody>
              {rows.map((c) => (
                <TR key={c.id} className="cursor-pointer" onClick={() => router.push(`/customers/${c.id}`)}>
                  <TD>
                    <Link
                      href={`/customers/${c.id}`}
                      onClick={(e) => e.stopPropagation()}
                      className="rounded font-semibold hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      {c.name}
                    </Link>
                    {c.status !== "active" && <StateBadge info={recordStatusInfo(c.status)} className="ms-2" />}
                  </TD>
                  <TD className="hidden text-end tabular sm:table-cell">{c.sites}</TD>
                  <TD className="text-end tabular">
                    {c.appliances}
                    {c.appliances > c.activated && (
                      <span className="block text-caption text-muted-foreground">{c.activated} activated</span>
                    )}
                  </TD>
                  <TD className="hidden text-end tabular md:table-cell">{c.licenses_active}</TD>
                  <TD>{c.attention > 0 ? <Badge tone="warn" dot>{c.attention}</Badge> : <span className="text-muted-foreground">—</span>}</TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      <DialogForm
        open={creating}
        onOpenChange={setCreating}
        title="New customer"
        description="An organisation that owns one or more hotels. Add its sites next."
        submitLabel="Create customer"
        busyLabel="Creating…"
        busy={busy}
        error={createErr}
        disabled={!name.trim()}
        onSubmit={create}
      >
        <Field label="Name" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
        </Field>
      </DialogForm>
    </PageShell>
  );
}
