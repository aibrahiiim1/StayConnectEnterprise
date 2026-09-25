"use client";

// ACCESS LOG — every time an operator read a code in the clear: single reveals and batch exports.
// Append-only on the server; nothing here can edit or remove an entry.

import * as React from "react";
import { ShieldCheck } from "lucide-react";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { FilterChips } from "@/components/ui/data";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { Table, TableWrap, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Toolbar } from "@/components/ui/page";
import { selectionWords, type VoucherReveal } from "@/lib/api/vouchers";
import { When } from "./shared";

type Kind = "all" | "REVEAL" | "EXPORT";

export function AccessLogTab({ reveals, error }: { reveals: VoucherReveal[] | null; error: unknown }) {
  const [kind, setKind] = React.useState<Kind>("all");
  const rows = (reveals ?? []).filter((r) => kind === "all" || r.action === kind);
  const count = (k: Kind) => (reveals ?? []).filter((r) => k === "all" || r.action === k).length;

  return (
    <Card>
      <CardHeader>
        <div className="space-y-0.5">
          <CardTitle>Who has read a code</CardTitle>
          <CardDescription>Every reveal and batch export, with the operator and reason. Latest 200 records.</CardDescription>
        </div>
      </CardHeader>
      <div className="px-4 py-3">
        <Toolbar>
          <FilterChips
            label="Record type"
            value={kind}
            onChange={setKind}
            options={[
              { value: "all", label: "All", count: count("all") },
              { value: "REVEAL", label: "Single card", count: count("REVEAL") },
              { value: "EXPORT", label: "Batch export", count: count("EXPORT") },
            ]}
          />
        </Toolbar>
      </div>
      <ErrorBanner err={error} className="mx-4" />
      {reveals === null && !error ? (
        <SkeletonRows rows={4} cols={5} />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<ShieldCheck />}
          title={kind === "all" ? "Nobody has read a code yet" : "No records of this type"}
          hint="When an operator shows a card's full code or exports a batch, it is recorded here."
        />
      ) : (
        <TableWrap>
          <Table>
            <THead>
              <TR>
                <TH>When</TH>
                <TH>What</TH>
                <TH className="text-right">Codes</TH>
                <TH>Who</TH>
                <TH>Why</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((r, i) => (
                <TR key={`${r.revealed_at}-${i}`}>
                  <TD className="whitespace-nowrap">
                    <When iso={r.revealed_at} />
                  </TD>
                  <TD>
                    {r.action === "REVEAL" ? (
                      <span className="inline-flex flex-wrap items-center gap-1.5">
                        One card <MonoId value={r.voucher_id} title="Card reference" />
                      </span>
                    ) : (
                      <span>Batch export {selectionWords(r.selection) && <span className="text-muted-foreground">· {selectionWords(r.selection)}</span>}</span>
                    )}
                  </TD>
                  <TD className="text-right tabular">{r.voucher_count.toLocaleString()}</TD>
                  <TD>{r.operator_label}</TD>
                  <TD className="max-w-[20rem] text-muted-foreground">{r.reason}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </TableWrap>
      )}
    </Card>
  );
}
