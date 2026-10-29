"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { FileUp, Server } from "lucide-react";
import {
  api, itemsOf, qs, withStepUp, type ApplianceRow, type Customer, type Items, type Overview, type RequestOptions, type Site,
} from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { FilterChips, SearchInput } from "@/components/ui/data";
import { refreshingClass } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { HelpList, HelpSection } from "@/components/help";
import { ApplianceTable, sortAppliances } from "@/components/appliance-table";
import { ActivateDialog } from "@/components/appliance-dialogs";
import { ListState } from "@/components/record-view";
import { usePermissions } from "@/lib/permissions";
import { usePoll } from "@/lib/use-poll";
import { keepUnchanged } from "@/lib/keep-unchanged";
import { useQueryState } from "@/lib/use-query-state";
import { ACTIVATION, ACTIVATION_ORDER, CONNECTION, CONNECTION_ORDER, LICENSE, LICENSE_ORDER } from "@/lib/status";
import { cn } from "@/lib/utils";

const PAGE_SIZE = 100;
const FILTER_SELECT = "w-auto min-w-36 max-w-56";

const FILTERS = ["activation", "connection", "license", "customer_id", "site_id", "q"] as const;

export default function AppliancesPage() {
  return (
    <Suspense fallback={null}>
      <Appliances />
    </Suspense>
  );
}

function Appliances() {
  const router = useRouter();
  const toast = useToast();
  const { can } = usePermissions();
  const [f, setF] = useQueryState(FILTERS);
  const [rows, setRows] = useState<ApplianceRow[] | null>(null);
  const [counts, setCounts] = useState<Overview["appliances"] | null>(null);
  const [customers, setCustomers] = useState<Customer[]>([]);
  const [sites, setSites] = useState<Site[]>([]);
  const [err, setErr] = useState<unknown>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [activate, setActivate] = useState<ApplianceRow | null>(null);
  const [importing, setImporting] = useState(false);
  // The code the appliance shows on its own screen; the file alone does not prove the operator is at the box.
  const [importCode, setImportCode] = useState("");
  const fileRef = useRef<HTMLInputElement | null>(null);

  // The list is paged by the server, 100 to a page (AD-55); a changed filter starts again at page 1.
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState<number | null>(null);
  const [nextPage, setNextPage] = useState<number | null>(null);
  const filterKey = qs({
    customer_id: f.customer_id, site_id: f.site_id, activation: f.activation,
    connection: f.connection, license: f.license, q: f.q,
  });
  useEffect(() => { setPage(1); }, [filterKey]);
  const query = qs({
    customer_id: f.customer_id, site_id: f.site_id, activation: f.activation,
    connection: f.connection, license: f.license, q: f.q, limit: PAGE_SIZE, page,
  });

  const load = useCallback(async (opts?: RequestOptions) => {
    setRefreshing(true);
    try {
      const r = await api.get<Items<ApplianceRow> & { total?: number; next_page?: number | null }>(`/cloud/v1/appliances${query}`, opts);
      setTotal(typeof r?.total === "number" ? r.total : null);
      setNextPage(typeof r?.next_page === "number" ? r.next_page : null);
      // Unchanged rows keep their objects, so only changed rows re-render (performance audit D8-M2).
      const next = sortAppliances(itemsOf(r));
      setRows((prev) => keepUnchanged(prev, next));
      setErr(null);
    } catch (e) {
      setErr(e);
      setRows((prev) => prev ?? []);
    } finally {
      setRefreshing(false);
    }
    // Overview counts already respect the server's role scope.
    api.get<Overview>("/cloud/v1/overview", opts).then((o) => setCounts(o.appliances)).catch(() => {});
  }, [query]);

  useEffect(() => { void load(); }, [load]);
  usePoll(load, 30_000);

  useEffect(() => {
    if (!can["customers.list"]) return;
    api.get<Items<Customer>>(`/cloud/v1/customers${qs({ status: "all" })}`)
      .then((r) => setCustomers(itemsOf(r)))
      .catch(() => {});
  }, [can]);

  useEffect(() => {
    setSites([]);
    if (!f.customer_id) return;
    api.get<Items<Site>>(`/cloud/v1/customers/${f.customer_id}/sites`)
      .then((r) => setSites(itemsOf(r)))
      .catch(() => {});
  }, [f.customer_id]);

  async function importRequest(file: File | null) {
    if (!file) return;
    if (!importCode.trim()) {
      toast.error("Enter the activation code", "Type the activation code shown on the appliance before importing its request.");
      if (fileRef.current) fileRef.current.value = "";
      return;
    }
    setImporting(true);
    try {
      let body: unknown;
      try {
        body = JSON.parse(await file.text());
      } catch {
        throw new Error("That file is not an activation request. Download it again from the appliance's Admin Console.");
      }
      // Importing creates an appliance, so it needs a password step-up like every appliance action.
      const row = await withStepUp(() => api.post<ApplianceRow>(`/cloud/v1/offline-activation/requests?activation_code=${encodeURIComponent(importCode.trim())}`, body));
      toast.success(`${row.serial} imported`, "Activate it, then download its activation package.");
      router.push(`/appliances/${row.id}`);
    } catch (e) {
      toast.error("Import failed", e instanceof Error ? e.message : String(e));
    } finally {
      setImporting(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  }

  const filtered = !!(f.activation || f.connection || f.license || f.customer_id || f.site_id || f.q);

  return (
    <PageShell width="wide">
      <PageHeader
        title="Appliances"
        description="Every appliance: where it is installed, whether it is connected and what its license allows."
        help={
          <>
            <HelpSection title="Getting a new appliance working">
              <HelpList
                items={[
                  <>Power it on with internet access. It registers itself and appears here as <strong>Waiting for activation</strong>, at the top of the list.</>,
                  <>Select <strong>Activate</strong>, choose the customer and site, and set its license.</>,
                  <>It picks up its license within a minute of its next contact and shows <strong>Activated</strong>.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Appliances without internet">
              <p>
                In the appliance&apos;s Admin Console, download its activation request, then use{" "}
                <strong>Import activation request</strong> here. Activate it, download its activation package from its
                page, and upload that file in the Admin Console.
              </p>
            </HelpSection>
          </>
        }
        actions={
          can["appliances.activate"] ? (
            <>
              <Input
                aria-label="Activation code shown on the appliance"
                placeholder="Activation code"
                className="w-40"
                value={importCode}
                maxLength={12}
                onChange={(e) => setImportCode(e.target.value)}
              />
              <input
                ref={fileRef}
                type="file"
                accept="application/json,.json"
                className="sr-only"
                tabIndex={-1}
                aria-hidden
                onChange={(e) => importRequest(e.target.files?.[0] ?? null)}
              />
              <Button variant="secondary" loading={importing} onClick={() => fileRef.current?.click()}>
                {!importing && <FileUp />} Import activation request
              </Button>
            </>
          ) : undefined
        }
      />

      <Card className="overflow-hidden">
        <FilterBar
          inset
          filters={
            <FilterChips
              label="Activation"
              value={f.activation}
              onChange={(v) => setF({ activation: v })}
              options={[
                { value: "", label: "All", count: counts?.total },
                ...ACTIVATION_ORDER.map((k) => ({
                  value: k,
                  label: k === "waiting" ? "Waiting" : ACTIVATION[k].label,
                  count: counts?.[k],
                  tone: ACTIVATION[k].tone === "default" ? undefined : (ACTIVATION[k].tone as "ok" | "warn" | "err" | "info"),
                })),
              ]}
            />
          }
        />
        <FilterBar
          className="border-b border-border px-4 py-3"
          search={
            <SearchInput
              value={f.q}
              onChange={(v) => setF({ q: v })}
              delay={300}
              placeholder="Search serial or hostname"
              label="Search appliances"
            />
          }
          actions={
            filtered ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setF({ activation: "", connection: "", license: "", customer_id: "", site_id: "", q: "" })}
              >
                Clear filters
              </Button>
            ) : undefined
          }
        >
          <Select aria-label="Connection" className={FILTER_SELECT} value={f.connection} onChange={(e) => setF({ connection: e.target.value })}>
            <option value="">Any connection</option>
            {CONNECTION_ORDER.map((k) => <option key={k} value={k}>{CONNECTION[k].label}</option>)}
          </Select>
          <Select aria-label="License" className={FILTER_SELECT} value={f.license} onChange={(e) => setF({ license: e.target.value })}>
            <option value="">Any license</option>
            {LICENSE_ORDER.map((k) => <option key={k} value={k}>{LICENSE[k].label}</option>)}
          </Select>
          {can["customers.list"] && (
            <Select
              aria-label="Customer"
              className={FILTER_SELECT}
              value={f.customer_id}
              onChange={(e) => setF({ customer_id: e.target.value, site_id: "" })}
            >
              <option value="">All customers</option>
              {customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </Select>
          )}
          {f.customer_id && (
            <Select aria-label="Site" className={FILTER_SELECT} value={f.site_id} onChange={(e) => setF({ site_id: e.target.value })}>
              <option value="">All sites</option>
              {sites.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </Select>
          )}
        </FilterBar>

        <ErrorBanner err={err} className="m-4" />

        <ListState
          rows={rows}
          cols={5}
          filtered={filtered}
          noMatch={
            <EmptyState
              title="No appliances match"
              hint="Nothing matches these filters."
              action={
                <Button variant="secondary" size="sm" onClick={() => setF({ activation: "", connection: "", license: "", customer_id: "", site_id: "", q: "" })}>
                  Clear filters
                </Button>
              }
            />
          }
          empty={
            <EmptyState
              icon={<Server />}
              title="No appliances yet"
              hint="A new appliance appears here by itself a minute after it is powered on with internet access."
            />
          }
        >
          <div className={cn(refreshing && refreshingClass)}>
            <ApplianceTable rows={rows ?? []} onActivate={can["appliances.activate"] ? setActivate : undefined} />
          </div>
          {(page > 1 || nextPage !== null) && (
            <div className="flex items-center justify-between gap-2 border-t border-border px-4 py-3 text-sm text-muted-foreground">
              <span>
                Page {page}{total !== null ? ` of ${Math.max(1, Math.ceil(total / PAGE_SIZE))} (${total} appliances)` : ""}
              </span>
              <span className="flex gap-2">
                <Button variant="secondary" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>Previous</Button>
                <Button variant="secondary" size="sm" disabled={nextPage === null} onClick={() => setPage(nextPage ?? page)}>Next</Button>
              </span>
            </div>
          )}
        </ListState>
      </Card>

      {activate && (
        <ActivateDialog
          appliance={activate}
          open={!!activate}
          onOpenChange={(v) => { if (!v) setActivate(null); }}
          onDone={load}
        />
      )}
    </PageShell>
  );
}
