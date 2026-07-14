"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { api, ListResp, Voucher, VoucherBatch, EDGE_BASE } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { ArrowLeft, Download } from "lucide-react";
import { formatRelative } from "@/lib/utils";

function toneFor(state: string): "info" | "default" | "err" | "warn" {
  switch (state) {
    case "active":    return "info";
    case "unused":    return "default";
    case "revoked":   return "err";
    case "expired":
    case "exhausted": return "warn";
    default:          return "default";
  }
}

export default function VoucherBatchDetail() {
  const { id } = useParams<{ id: string }>();
  const [batch, setBatch] = useState<VoucherBatch | null>(null);
  const [vouchers, setVouchers] = useState<Voucher[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!id) return;
    (async () => {
      try {
        const [b, vs] = await Promise.all([
          api.get<VoucherBatch>(`/voucher-batches/${id}`),
          api.get<ListResp<Voucher>>(`/voucher-batches/${id}/codes?limit=500`),
        ]);
        setBatch(b);
        setVouchers(vs.data);
      } catch (e: any) {
        setErr(e?.message ?? "Failed to load");
      }
    })();
  }, [id]);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <Link href="/voucher-batches" className="text-sm text-muted hover:text-text inline-flex items-center gap-1 mb-4">
        <ArrowLeft size={14} /> Back to batches
      </Link>

      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Voucher batch</div>
          <h1 className="text-2xl font-semibold">{batch?.name || "—"}</h1>
          {batch && <div className="text-xs text-muted font-mono">{batch.id}</div>}
        </div>
        <a
          href={`${EDGE_BASE}/voucher-batches/${id}/codes.csv`}
          target="_blank" rel="noopener"
          className="inline-flex items-center gap-2 h-9 px-4 text-sm rounded-md bg-panel2 border border-border hover:bg-[#222735]"
        >
          <Download size={14} /> Download CSV
        </a>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <Card>
        <CardHeader>
          <CardTitle>{vouchers ? `${vouchers.length} codes` : "Loading…"}</CardTitle>
        </CardHeader>
        <CardBody className="p-0">
          {vouchers && vouchers.length > 0 && (
            <Table>
              <THead>
                <TR>
                  <TH>Code</TH>
                  <TH>State</TH>
                  <TH>Issued</TH>
                  <TH>Activated</TH>
                </TR>
              </THead>
              <tbody>
                {vouchers.map((v) => (
                  <TR key={v.id}>
                    <TD className="font-mono">{v.code_display}</TD>
                    <TD><Badge tone={toneFor(v.state) as any}>{v.state}</Badge></TD>
                    <TD className="text-muted">{formatRelative(v.issued_at)}</TD>
                    <TD className="text-muted">{v.activated_at ? formatRelative(v.activated_at) : "—"}</TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
