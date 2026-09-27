"use client";

import { Suspense, useEffect, useState } from "react";
import { X } from "lucide-react";
import { api, itemsOf, qs, type Customer, type Items } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { AuditLog } from "@/components/audit-log";
import { useQueryState } from "@/lib/use-query-state";

const FILTERS = ["customer_id", "appliance_id", "action", "since"] as const;

export default function AuditPage() {
  return (
    <Suspense fallback={null}>
      <Audit />
    </Suspense>
  );
}

function Audit() {
  const [f, setF] = useQueryState(FILTERS);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [action, setAction] = useState(f.action);

  useEffect(() => setAction(f.action), [f.action]);
  useEffect(() => {
    api.get<Items<Customer>>(`/cloud/v1/customers${qs({ status: "all" })}`)
      .then((r) => setCustomers(itemsOf(r)))
      .catch(() => {});
  }, []);

  // The date input gives a day; the API takes a moment. The day starts at local midnight.
  const since = f.since ? new Date(`${f.since}T00:00:00`).toISOString() : undefined;

  return (
    <PageShell width="wide">
      <PageHeader
        title="Audit log"
        description="Every change made in Central and by the appliances, newest first. Entries cannot be edited or removed."
      />

      <Card>
        <form
          className="flex flex-wrap items-end gap-3 border-b border-border px-4 py-3"
          onSubmit={(e) => { e.preventDefault(); setF({ action: action.trim() }); }}
        >
          <Field label="Customer" className="w-full sm:w-56">
            <Select value={f.customer_id} onChange={(e) => setF({ customer_id: e.target.value })}>
              <option value="">All customers</option>
              {customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </Select>
          </Field>
          <Field label="Action" hint="For example license.revoked" className="w-full sm:w-56">
            <Input value={action} onChange={(e) => setAction(e.target.value)} onBlur={() => setF({ action: action.trim() })} />
          </Field>
          <Field label="From" className="w-full sm:w-44">
            <Input type="date" value={f.since} onChange={(e) => setF({ since: e.target.value })} />
          </Field>
          {f.appliance_id && (
            <Button type="button" variant="subtle" size="sm" className="mb-1" onClick={() => setF({ appliance_id: "" })}>
              One appliance only <X /> <span className="sr-only">Show all appliances</span>
            </Button>
          )}
          {(f.customer_id || f.action || f.since || f.appliance_id) && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="mb-1"
              onClick={() => { setAction(""); setF({ customer_id: "", action: "", since: "", appliance_id: "" }); }}
            >
              Clear filters
            </Button>
          )}
          <button type="submit" className="sr-only">Apply filters</button>
        </form>
        <AuditLog filters={{ customer_id: f.customer_id, appliance_id: f.appliance_id, action: f.action, since }} />
      </Card>
    </PageShell>
  );
}
