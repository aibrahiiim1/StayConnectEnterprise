"use client";

import { Suspense, useCallback, useEffect, useState } from "react";
import { BadgeCheck } from "lucide-react";
import { api, itemsOf, qs, type Items, type LicenseRow, type Overview } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { FilterChips, SearchInput } from "@/components/ui/data";
import { HelpList, HelpSection } from "@/components/help";
import { LicenseTable } from "@/components/license-table";
import { ListState } from "@/components/record-view";
import { useQueryState } from "@/lib/use-query-state";
import { LICENSE, LICENSE_ORDER } from "@/lib/status";

const FILTERS = ["state", "q"] as const;
const STATES = LICENSE_ORDER.filter((s) => s !== "none");

export default function LicensesPage() {
  return (
    <Suspense fallback={null}>
      <Licenses />
    </Suspense>
  );
}

function Licenses() {
  const [f, setF] = useQueryState(FILTERS);
  const [rows, setRows] = useState<LicenseRow[] | null>(null);
  const [counts, setCounts] = useState<Overview["licenses"] | null>(null);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    setErr(null);
    try {
      const r = await api.get<Items<LicenseRow>>(`/cloud/v1/licenses${qs({ state: f.state, q: f.q })}`);
      setRows(itemsOf(r));
    } catch (e) {
      setErr(e);
      setRows([]);
    }
  }, [f.state, f.q]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    api.get<Overview>("/cloud/v1/overview").then((o) => setCounts(o.licenses)).catch(() => {});
  }, []);

  const total = counts ? STATES.reduce((n, s) => n + (counts[s] ?? 0), 0) : undefined;

  return (
    <PageShell width="wide">
      <PageHeader
        title="Licenses"
        description="Every appliance's current license. Open one to renew, change, suspend or revoke it."
        help={
          <>
            <HelpSection title="License states">
              <HelpList items={STATES.map((s) => <><strong>{LICENSE[s].label}</strong>: {LICENSE[s].explain}</>)} />
            </HelpSection>
            <HelpSection title="Changing a license">
              <p>
                Licenses are changed on their appliance&apos;s page. Each change issues a new version; the appliance picks
                it up on its next contact. The appliance page lists every earlier version.
              </p>
            </HelpSection>
          </>
        }
      />

      <Card className="overflow-hidden">
        <FilterBar
          inset
          filters={
            <FilterChips
              label="License state"
              value={f.state}
              onChange={(v) => setF({ state: v })}
              options={[
                { value: "", label: "All", count: total },
                ...STATES.map((s) => ({
                  value: s,
                  label: LICENSE[s].label,
                  count: counts?.[s],
                  tone: LICENSE[s].tone === "default" ? undefined : (LICENSE[s].tone as "ok" | "warn" | "err" | "info"),
                })),
              ]}
            />
          }
          search={
            <SearchInput value={f.q} onChange={(v) => setF({ q: v })} delay={300} placeholder="Search serial or customer" label="Search licenses" />
          }
        />

        <ErrorBanner err={err} className="m-4" />

        <ListState
          rows={rows}
          cols={5}
          filtered={!!(f.state || f.q)}
          noMatch={
            <EmptyState
              title="No licenses match"
              hint="Nothing matches these filters."
              action={<Button variant="secondary" size="sm" onClick={() => setF({ state: "", q: "" })}>Clear filters</Button>}
            />
          }
          empty={<EmptyState icon={<BadgeCheck />} title="No licenses yet" hint="A license is issued when an appliance is activated." />}
        >
          <LicenseTable rows={rows ?? []} />
        </ListState>
      </Card>
    </PageShell>
  );
}
