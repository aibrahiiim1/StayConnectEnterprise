"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import type { LicenseRow, LicenseState } from "@/lib/api";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { LicenseBadge } from "@/components/status-badge";
import { ROW_LINK } from "@/components/appliance-table";
import { formatLicenseDay, licenseSentence } from "@/lib/status";

/** Empty grants display as "Core only". */
export function modulesSummary(ids: string[] | null | undefined): string {
  return ids && ids.length ? ids.map((id) => id.replace(/_/g, " ")).join(", ") : "Core only";
}

export function LicenseTable({ rows, showCustomer = true }: { rows: LicenseRow[]; showCustomer?: boolean }) {
  const router = useRouter();
  return (
    <Table aria-label="Licenses">
      <THead>
        <TR>
          <TH>Appliance</TH>
          {showCustomer && <TH className="hidden md:table-cell">Customer · site</TH>}
          <TH>State</TH>
          <TH numeric className="hidden sm:table-cell">Clients at once</TH>
          <TH className="hidden md:table-cell">Modules</TH>
          <TH>Valid until</TH>
          <TH numeric className="hidden lg:table-cell">Version</TH>
        </TR>
      </THead>
      <TBody>
        {rows.map((l) => {
          const href = `/appliances/${l.appliance_id}`;
          return (
            <TR key={l.id} onClick={() => router.push(href)}>
              <TD nowrap>
                <Link href={href} onClick={(e) => e.stopPropagation()} className={ROW_LINK}>
                  {l.serial}
                </Link>
              </TD>
              {showCustomer && (
                <TD className="hidden md:table-cell">
                  <div className="truncate">{l.customer_name ?? "—"}</div>
                  <div className="truncate text-xs text-muted-foreground">{l.site_name ?? "—"}</div>
                </TD>
              )}
              <TD nowrap><LicenseBadge value={l.state} /></TD>
              <TD numeric className="hidden sm:table-cell">{l.max_concurrent_online_guests?.toLocaleString() ?? "—"}</TD>
              <TD truncate muted className="hidden max-w-[14rem] md:table-cell" title={modulesSummary(l.modules)}>
                {modulesSummary(l.modules)}
              </TD>
              <TD nowrap>
                {formatLicenseDay(l.valid_until)}
                {l.state !== "active" && l.state !== "superseded" && (
                  <div className="text-xs text-muted-foreground">
                    {licenseSentence({ state: l.state as LicenseState, valid_until: l.valid_until, grace_ends_at: l.grace_ends_at })}
                  </div>
                )}
              </TD>
              <TD numeric muted className="hidden lg:table-cell">v{l.license_version ?? "—"}</TD>
            </TR>
          );
        })}
      </TBody>
    </Table>
  );
}
