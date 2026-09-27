"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { ScrollText } from "lucide-react";
import { api, itemsOf, qs, type AuditEntry, type AuditPage } from "@/lib/api";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { actionTone, actionWords, ago, formatDateTime } from "@/lib/status";

export type AuditFilters = {
  customer_id?: string;
  appliance_id?: string;
  action?: string;
  since?: string;
};

const PAGE = 50;

function Details({ payload }: { payload: Record<string, unknown> }) {
  const n = Object.keys(payload).length;
  return (
    <details className="text-xs">
      <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
        {n} {n === 1 ? "detail" : "details"}
      </summary>
      <pre className="mt-1 max-h-48 max-w-md overflow-auto whitespace-pre-wrap break-all rounded bg-surface p-2 font-mono text-muted-foreground">
        {JSON.stringify(payload, null, 2)}
      </pre>
    </details>
  );
}

/**
 * The audit log, for the whole platform (System → Audit log) or one customer (its Activity tab). Newest first,
 * 50 at a time; "Show older" follows the server's cursor.
 */
export function AuditLog({ filters, showCustomer = true }: { filters: AuditFilters; showCustomer?: boolean }) {
  const [rows, setRows] = useState<AuditEntry[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [more, setMore] = useState(false);
  const key = JSON.stringify(filters);

  const fetchPage = useCallback(
    (after?: string) => api.get<AuditPage>(`/cloud/v1/audit${qs({ ...filters, limit: PAGE, cursor: after })}`),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key],
  );

  useEffect(() => {
    let cancelled = false;
    setRows(null);
    setErr(null);
    fetchPage()
      .then((r) => {
        if (cancelled) return;
        setRows(itemsOf(r));
        setCursor(r.next_cursor ?? null);
      })
      .catch((e) => {
        if (cancelled) return;
        setErr(e);
        setRows([]);
      });
    return () => { cancelled = true; };
  }, [fetchPage]);

  async function older() {
    if (!cursor) return;
    setMore(true);
    try {
      const r = await fetchPage(cursor);
      setRows((prev) => [...(prev ?? []), ...itemsOf(r)]);
      setCursor(r.next_cursor ?? null);
    } catch (e) {
      setErr(e);
    } finally {
      setMore(false);
    }
  }

  return (
    <>
      <ErrorBanner err={err} className="m-4" />
      {rows === null ? (
        <SkeletonRows rows={6} cols={4} />
      ) : rows.length === 0 ? (
        <EmptyState icon={<ScrollText />} title="No activity" hint="Nothing has been recorded for these filters." />
      ) : (
        <>
          <Table aria-label="Audit log">
            <THead>
              <TR>
                <TH>When</TH>
                <TH>What</TH>
                {showCustomer && <TH className="hidden md:table-cell">Customer</TH>}
                <TH className="hidden sm:table-cell">Appliance</TH>
                <TH className="hidden lg:table-cell">By</TH>
                <TH className="hidden xl:table-cell">Details</TH>
              </TR>
            </THead>
            <tbody>
              {rows.map((e, i) => (
                <TR key={e.id ?? `${e.ts}-${i}`}>
                  <TD className="whitespace-nowrap text-muted-foreground">
                    <time dateTime={e.ts} title={formatDateTime(e.ts)}>{ago(e.ts)}</time>
                  </TD>
                  <TD><Badge tone={actionTone(e.action)}>{actionWords(e.action)}</Badge></TD>
                  {showCustomer && (
                    <TD className="hidden md:table-cell">
                      {e.customer_id ? (
                        <Link href={`/customers/${e.customer_id}`} className="underline-offset-2 hover:underline">
                          {e.customer_name ?? "Customer"}
                        </Link>
                      ) : <span className="text-muted-foreground">Platform</span>}
                    </TD>
                  )}
                  <TD className="hidden sm:table-cell">
                    {e.appliance_id ? (
                      <Link href={`/appliances/${e.appliance_id}`} className="font-mono text-xs underline-offset-2 hover:underline">
                        {e.serial ?? "Appliance"}
                      </Link>
                    ) : <span className="text-muted-foreground">—</span>}
                  </TD>
                  <TD className="hidden text-muted-foreground lg:table-cell">
                    {e.actor_email ?? (e.actor_type === "appliance" ? "The appliance" : e.actor_type === "system" ? "Central" : e.actor_type ?? "—")}
                  </TD>
                  <TD className="hidden xl:table-cell">
                    {e.payload && Object.keys(e.payload).length > 0 ? <Details payload={e.payload} /> : "—"}
                  </TD>
                </TR>
              ))}
            </tbody>
          </Table>
          {cursor && (
            <div className="flex justify-center border-t border-border p-3">
              <Button variant="secondary" size="sm" disabled={more} onClick={older}>
                {more ? "Loading…" : "Show older"}
              </Button>
            </div>
          )}
        </>
      )}
    </>
  );
}
