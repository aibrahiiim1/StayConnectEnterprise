"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import type { LicenseRow, LicenseState } from "@/lib/api";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { LicenseBadge } from "@/components/status-badge";
import { formatLicenseDay, licenseSentence } from "@/lib/status";

/** A license's modules in words: "Core only" when it authorises none. */
export function modulesSummary(ids: string[] | null | undefined): string {
  return ids && ids.length ? ids.map((id) => id.replace(/_/g, " ")).join(", ") : "Core only";
}

/** Every license list: the fleet list and a customer's Licenses tab. A row opens the appliance it belongs to. */
export function LicenseTable({ rows, showCustomer = true }: { rows: LicenseRow[]; showCustomer?: boolean }) {
  const router = useRouter();
  return (
    <Table aria-label="Licenses">
      <THead>
        <TR>
          <TH>Appliance</TH>
          {showCustomer && <TH className="hidden md:table-cell">Customer · site</TH>}
          <TH>State</TH>
          <TH className="hidden sm:table-cell">Clients online at once</TH>
          <TH className="hidden md:table-cell">Modules</TH>
          <TH>Valid until</TH>
          <TH className="hidden lg:table-cell">Version</TH>
        </TR>
      </THead>
      <tbody>
        {rows.map((l) => {
          const href = `/appliances/${l.appliance_id}`;
          return (
            <TR key={l.id} className="cursor-pointer" onClick={() => router.push(href)}>
              <TD>
                <Link
                  href={href}
                  onClick={(e) => e.stopPropagation()}
                  className="rounded font-mono text-sm font-semibold hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {l.serial}
                </Link>
              </TD>
              {showCustomer && (
                <TD className="hidden md:table-cell">
                  <div className="text-sm">{l.customer_name ?? "—"}</div>
                  <div className="text-caption text-muted-foreground">{l.site_name ?? "—"}</div>
                </TD>
              )}
              <TD><LicenseBadge value={l.state} /></TD>
              <TD className="hidden tabular sm:table-cell">{l.max_concurrent_online_guests?.toLocaleString() ?? "—"}</TD>
              <TD className="hidden text-sm text-muted-foreground md:table-cell">{modulesSummary(l.modules)}</TD>
              <TD>
                <div>{formatLicenseDay(l.valid_until)}</div>
                {l.state !== "active" && l.state !== "superseded" && (
                  <div className="text-caption text-muted-foreground">
                    {licenseSentence({ state: l.state as LicenseState, valid_until: l.valid_until, grace_ends_at: l.grace_ends_at })}
                  </div>
                )}
              </TD>
              <TD className="hidden tabular text-muted-foreground lg:table-cell">v{l.license_version ?? "—"}</TD>
            </TR>
          );
        })}
      </tbody>
    </Table>
  );
}
