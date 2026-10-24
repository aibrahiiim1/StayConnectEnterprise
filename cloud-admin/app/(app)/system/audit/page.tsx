"use client";

import { Suspense, useEffect, useState } from "react";
import { X } from "lucide-react";
import { api, itemsOf, qs, type Customer, type Items } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { HelpList, HelpSection } from "@/components/help";
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

  // Convert calendar-day input to the local midnight instant expected by the API.
  const since = f.since ? new Date(`${f.since}T00:00:00`).toISOString() : undefined;
  const filtered = !!(f.customer_id || f.action || f.since || f.appliance_id);
  const applyAction = () => setF({ action: action.trim() });

  return (
    <PageShell width="wide">
      <PageHeader
        title="Audit log"
        description="Every change made in Central and by the appliances, newest first."
        help={
          <HelpSection title="About the audit log">
            <HelpList
              items={[
                <>Entries cannot be edited or removed. How long they are kept is set under <strong>Team</strong>, Data retention.</>,
                <>Select an entry to see everything recorded with it, including the address it came from.</>,
                <>The action filter takes an action code, for example <span className="font-mono">license.revoked</span>.</>,
              ]}
            />
          </HelpSection>
        }
      />

      <Card className="overflow-hidden">
        <FilterBar
          inset
          actions={
            filtered ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => { setAction(""); setF({ customer_id: "", action: "", since: "", appliance_id: "" }); }}
              >
                Clear filters
              </Button>
            ) : undefined
          }
        >
          <Select
            aria-label="Customer"
            className="w-full sm:w-52"
            value={f.customer_id}
            onChange={(e) => setF({ customer_id: e.target.value })}
          >
            <option value="">All customers</option>
            {customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </Select>
          <Input
            aria-label="Action"
            className="w-full sm:w-52"
            placeholder="Action, e.g. license.revoked"
            autoComplete="off"
            spellCheck={false}
            value={action}
            onChange={(e) => setAction(e.target.value)}
            onBlur={applyAction}
            onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); applyAction(); } }}
          />
          <Input
            aria-label="From"
            type="date"
            className="w-full sm:w-44"
            value={f.since}
            onChange={(e) => setF({ since: e.target.value })}
          />
          {f.appliance_id && (
            <Button type="button" variant="subtle" size="sm" onClick={() => setF({ appliance_id: "" })}>
              One appliance only <X /> <span className="sr-only">Show all appliances</span>
            </Button>
          )}
        </FilterBar>
        <AuditLog filters={{ customer_id: f.customer_id, appliance_id: f.appliance_id, action: f.action, since }} />
      </Card>
    </PageShell>
  );
}
