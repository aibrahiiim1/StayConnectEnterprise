"use client";

import { useEffect, useState } from "react";
import { Filter, ScrollText } from "lucide-react";
import { api, AuditEntry, ListResp } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { RoleRestricted } from "@/components/role-restricted";
import { usePermissions } from "@/lib/permissions";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { Pagination } from "@/components/ui/data";
import { CustomerScope, SelectCustomerCard } from "@/components/customer-scope";
import { formatDate } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";

const ACTION_TONE: Record<string, "ok" | "warn" | "err" | "info" | "default"> = {
  created: "ok",
  updated: "info",
  deleted: "err",
  archived: "warn",
  revoked: "warn",
  disabled: "warn",
  password_reset: "warn",
  role_added: "info",
  role_removed: "warn",
  plan_changed: "info",
  disconnected: "warn",
};

function tone(action: string) {
  const verb = action.split(".")[1] ?? "";
  return ACTION_TONE[verb] ?? "default";
}

// The server answers with at most its default 100 entries and takes no offset, so paging is over that answer.
const PAGE_SIZE = 25;

function hasPayload(e: AuditEntry) {
  return !!e.payload && Object.keys(e.payload).length > 0;
}

function PayloadDetails({ payload }: { payload: Record<string, unknown> }) {
  const n = Object.keys(payload).length;
  return (
    <details className="text-xs">
      <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
        {n} {n === 1 ? "field" : "fields"}
      </summary>
      <pre className="mt-1 max-h-48 overflow-auto rounded bg-surface p-2 font-mono text-muted-foreground">
        {JSON.stringify(payload, null, 2)}
      </pre>
    </details>
  );
}

export default function AuditPage() {
  // Reading needs "audit.read" (lib/permissions.ts); the server refuses this page's list to other roles.
  const { can } = usePermissions();
  const canRead = can["audit.read"];
  // The audit log is per-customer. It requires a concrete customer in the Customer context; "All customers"
  // shows a prompt.
  const { selectedTenantId: tenantID, ready } = useCustomer();
  const allCustomers = tenantID === "";
  const [rows, setRows] = useState<AuditEntry[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [actionFilter, setActionFilter] = useState("");
  const [offset, setOffset] = useState(0);

  async function load(filter: string = actionFilter) {
    if (!ready) return;
    if (allCustomers) { setRows(null); return; }
    setRows(null); setErr(null); setOffset(0);
    const q = new URLSearchParams();
    if (filter) q.set("action", filter);
    try {
      const r = await api.get<ListResp<AuditEntry>>(`/v1/tenants/${tenantID}/audit?${q.toString()}`);
      setRows(r.data);
    } catch (e) { setErr(e); setRows([]); }
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { load(); }, [ready, tenantID]);

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Administration"
        title="Audit log"
        icon={<ScrollText />}
        description="Who did what for this customer in the last 7 days."
        help={
          <>
            <HelpSection title="About the audit log">
              <p>
                The audit log is kept per customer: select a customer in the sidebar to see it. It covers the last
                7 days, and entries are never edited or removed.
              </p>
            </HelpSection>
            <HelpSection title="Filtering">
              <HelpList
                items={[
                  <>Enter one or more actions, comma-separated, for example <span className="font-mono">site.created,operator.disabled</span>, then <strong>Apply</strong>.</>,
                  <>Leave the filter blank to see every action.</>,
                  <>Open <strong>Payload</strong> on a row to see the recorded details.</>,
                ]}
              />
            </HelpSection>
          </>
        }
      >
        <CustomerScope />
      </PageHeader>

      {!canRead ? (
        <RoleRestricted what="The audit log is kept per customer, and your sign-in has none." />
      ) : (
      <>
      <ErrorBanner err={err} />

      {allCustomers ? (
        <SelectCustomerCard what="The audit log is kept per customer." />
      ) : (
        <Card>
          <CardHeader>
            <div className="space-y-0.5">
              <CardTitle>{rows ? `${rows.length} ${rows.length === 1 ? "event" : "events"}` : "Events"}</CardTitle>
              <CardDescription>Last 7 days.</CardDescription>
            </div>
            <form
              onSubmit={(e) => { e.preventDefault(); load(); }}
              className="flex w-full flex-wrap items-end gap-2 sm:w-auto"
              role="search"
            >
              <Field label="Filter by action" hint={<>Comma-separated, e.g. <span className="font-mono">site.created,operator.disabled</span></>} className="w-full sm:w-96">
                <Input value={actionFilter} onChange={(e) => setActionFilter(e.target.value)} placeholder="Leave blank for all" />
              </Field>
              <Button type="submit" variant="secondary" className="mb-6"><Filter /> Apply</Button>
            </form>
          </CardHeader>
          {rows === null ? (
            <SkeletonRows rows={6} cols={6} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<ScrollText />}
              title={actionFilter ? "No events match this filter" : "No audit events in this window"}
              hint={actionFilter ? "Clear the filter to see every action." : "Actions on this customer's records appear here."}
              action={actionFilter ? <Button variant="secondary" onClick={() => { setActionFilter(""); load(""); }}>Clear filter</Button> : undefined}
            />
          ) : (
            <>
            <Table label="Audit events">
              <THead>
                <TR>
                  <TH>When</TH>
                  <TH>Actor</TH>
                  <TH>Action</TH>
                  <TH className="hidden md:table-cell">Target</TH>
                  <TH className="hidden lg:table-cell">IP</TH>
                  <TH className="hidden md:table-cell">Payload</TH>
                </TR>
              </THead>
              <tbody>
                {rows.slice(offset, offset + PAGE_SIZE).map((e, i) => (
                  // Entries carry no id; the timestamp plus the absolute position is stable within one answer.
                  <TR key={`${e.ts}-${offset + i}`}>
                    <TD className="whitespace-nowrap align-top text-xs text-muted-foreground tabular md:align-middle">{formatDate(e.ts)}</TD>
                    <TD className="align-top md:align-middle">
                      <div className="text-sm">{e.actor_type}</div>
                      {e.actor_id && <MonoId value={e.actor_id} title="Actor id" />}
                    </TD>
                    <TD className="align-top md:align-middle">
                      <Badge tone={tone(e.action)} className="font-mono">{e.action}</Badge>
                      {/* Below md the Target and Payload columns are hidden (IP below lg), so they stack under the
                          action instead of disappearing. */}
                      <dl className="mt-2 space-y-1.5 text-xs empty:hidden lg:hidden">
                        {e.target_type && (
                          <div className="md:hidden">
                            <dt className="sr-only">Target</dt>
                            <dd>
                              <span className="text-sm">{e.target_type}</span>
                              {e.target_id && <div><MonoId value={e.target_id} title="Target id" /></div>}
                            </dd>
                          </div>
                        )}
                        {e.ip && (
                          <div className="flex gap-1.5">
                            <dt className="text-muted-foreground">IP</dt>
                            <dd className="font-mono text-muted-foreground">{e.ip}</dd>
                          </div>
                        )}
                        {hasPayload(e) && (
                          <div className="md:hidden">
                            <dt className="sr-only">Payload</dt>
                            <dd><PayloadDetails payload={e.payload!} /></dd>
                          </div>
                        )}
                      </dl>
                    </TD>
                    <TD className="hidden md:table-cell">
                      {e.target_type ? (
                        <>
                          <div className="text-sm">{e.target_type}</div>
                          {e.target_id && <MonoId value={e.target_id} title="Target id" />}
                        </>
                      ) : "—"}
                    </TD>
                    <TD className="hidden font-mono text-xs text-muted-foreground lg:table-cell">{e.ip ?? "—"}</TD>
                    <TD className="hidden max-w-md md:table-cell">
                      {hasPayload(e) ? <PayloadDetails payload={e.payload!} /> : <span className="text-muted-foreground">—</span>}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
            {rows.length > PAGE_SIZE && (
              <Pagination
                className="border-t border-border px-4 py-3"
                offset={offset}
                limit={PAGE_SIZE}
                shown={Math.min(PAGE_SIZE, rows.length - offset)}
                total={rows.length}
                onChange={setOffset}
              />
            )}
            </>
          )}
        </Card>
      )}
      </>
      )}
    </PageShell>
  );
}
