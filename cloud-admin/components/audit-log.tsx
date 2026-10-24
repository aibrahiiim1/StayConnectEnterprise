"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { ScrollText } from "lucide-react";
import { api, itemsOf, qs, type AuditEntry, type AuditPage } from "@/lib/api";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { ListState, PayloadView, hasPayload } from "@/components/record-view";
import { actionTone, actionWords, ago, formatDateTime, sentenceCase } from "@/lib/status";

export type AuditFilters = {
  customer_id?: string;
  appliance_id?: string;
  action?: string;
  since?: string;
};

const PAGE = 50;
const LINK = "rounded underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function actorOf(e: AuditEntry): string {
  return e.actor_email ?? (e.actor_type === "appliance" ? "The appliance" : e.actor_type === "system" ? "Central" : e.actor_type ?? "—");
}

/** Entries are append-only: the list shows who did what and when; everything recorded opens in a drawer. */
export function AuditLog({ filters, showCustomer = true }: { filters: AuditFilters; showCustomer?: boolean }) {
  const [rows, setRows] = useState<AuditEntry[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [more, setMore] = useState(false);
  const [selected, setSelected] = useState<AuditEntry | null>(null);
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
      <ListState
        rows={rows}
        cols={4}
        empty={<EmptyState size="sm" icon={<ScrollText />} title="No activity" hint="Nothing has been recorded for these filters." />}
      >
        <Table aria-label="Audit log">
          <THead>
            <TR>
              <TH>When</TH>
              <TH>What</TH>
              {showCustomer && <TH className="hidden md:table-cell">Customer</TH>}
              <TH className="hidden sm:table-cell">Appliance</TH>
              <TH className="hidden lg:table-cell">By</TH>
            </TR>
          </THead>
          <TBody>
            {(rows ?? []).map((e, i) => (
              <TR key={e.id ?? `${e.ts}-${i}`} onClick={() => setSelected(e)} selected={selected === e}>
                <TD nowrap muted>
                  <time dateTime={e.ts} title={formatDateTime(e.ts)}>{ago(e.ts)}</time>
                </TD>
                <TD nowrap>
                  <Badge tone={actionTone(e.action)}>{actionWords(e.action)}</Badge>
                </TD>
                {showCustomer && (
                  <TD className="hidden md:table-cell">
                    {e.customer_id ? (
                      <Link href={`/customers/${e.customer_id}`} onClick={(ev) => ev.stopPropagation()} className={LINK}>
                        {e.customer_name ?? "Customer"}
                      </Link>
                    ) : <span className="text-muted-foreground">Platform</span>}
                  </TD>
                )}
                <TD nowrap className="hidden sm:table-cell">
                  {e.appliance_id ? (
                    <Link href={`/appliances/${e.appliance_id}`} onClick={(ev) => ev.stopPropagation()} className={`${LINK} font-mono text-xs`}>
                      {e.serial ?? "Appliance"}
                    </Link>
                  ) : <span className="text-muted-foreground">—</span>}
                </TD>
                <TD truncate muted className="hidden lg:table-cell" title={actorOf(e)}>{actorOf(e)}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
        {cursor && (
          <div className="flex justify-center border-t border-border p-3">
            <Button variant="secondary" size="sm" loading={more} onClick={older}>
              Show older
            </Button>
          </div>
        )}
      </ListState>

      <DetailsDrawer
        open={!!selected}
        onOpenChange={(v) => { if (!v) setSelected(null); }}
        eyebrow="Audit entry"
        title={selected ? actionWords(selected.action) : ""}
        description={selected ? formatDateTime(selected.ts) : undefined}
      >
        {selected && (
          <>
            <DetailsSection title="Entry">
              <DescriptionList
                items={[
                  { label: "When", value: formatDateTime(selected.ts) },
                  { label: "By", value: actorOf(selected) },
                  { label: "From address", value: selected.ip ? <span className="font-mono text-xs">{selected.ip}</span> : null, hidden: !selected.ip },
                  {
                    label: "Customer",
                    value: selected.customer_id ? (
                      <Link href={`/customers/${selected.customer_id}`} className="text-primary underline-offset-2 hover:underline">
                        {selected.customer_name ?? "Customer"}
                      </Link>
                    ) : "Platform",
                  },
                  {
                    label: "Appliance",
                    value: selected.appliance_id ? (
                      <Link href={`/appliances/${selected.appliance_id}`} className="font-mono text-xs text-primary underline-offset-2 hover:underline">
                        {selected.serial ?? "Appliance"}
                      </Link>
                    ) : null,
                    hidden: !selected.appliance_id,
                  },
                  {
                    label: "Target",
                    value: selected.target_type ? `${sentenceCase(selected.target_type)}${selected.target_id ? ` ${selected.target_id}` : ""}` : null,
                    hidden: !selected.target_type,
                  },
                  { label: "Action", value: <span className="font-mono text-xs">{selected.action}</span> },
                ]}
              />
            </DetailsSection>
            {hasPayload(selected.payload) && (
              <DetailsSection title="Recorded details">
                <PayloadView payload={selected.payload} />
              </DetailsSection>
            )}
          </>
        )}
      </DetailsDrawer>
    </>
  );
}
