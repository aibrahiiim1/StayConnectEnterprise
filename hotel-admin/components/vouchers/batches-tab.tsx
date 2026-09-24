"use client";

// BATCHES — one row per print run, counted by the server over the WHOLE batch.
//
// The previous screen built its batch list from whichever page of cards happened to be loaded, labelled each
// with "N cards in view", and then exported the entire batch. So the number next to the button was not the
// number the button acted on, and a batch beyond the loaded page did not appear at all. These counts come
// from the server's aggregate, and the export dialog states exactly how many codes it will recover.

import * as React from "react";
import { Download, Layers, List } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { KeyValueGrid, MetricStrip, Pagination } from "@/components/ui/data";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { Segmented } from "@/components/ui/tabs";
import { Table, TableWrap, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import {
  batchLabel,
  validityWords,
  type ExportedBatch,
  type VoucherBatch,
  type VoucherBatchesResp,
} from "@/lib/api/vouchers";
import { CodesHeld } from "./codes-held";
import { reasonProblem, stepUpError, When } from "./shared";

const PAGE = 25;

export function BatchesTab({
  canRevealCodes,
  reloadKey,
  onViewCards,
  hotelName,
  focusBatch,
}: {
  canRevealCodes: boolean;
  reloadKey: number;
  onViewCards: (batchId: string) => void;
  hotelName?: string;
  /** A batch to open straight away (from a card's "Batch" link). */
  focusBatch?: { id: string } | null;
}) {
  const [data, setData] = React.useState<VoucherBatchesResp | null>(null);
  const [err, setErr] = React.useState<unknown>(null);
  const [offset, setOffset] = React.useState(0);
  const [open, setOpen] = React.useState<VoucherBatch | null>(null);

  const load = React.useCallback(() => {
    setErr(null);
    api
      .get<VoucherBatchesResp>(`/vouchers/batches?limit=${PAGE}&offset=${offset}`)
      .then(setData)
      .catch((e) => {
        setErr(e);
        setData({ batches: [] });
      });
  }, [offset]);

  React.useEffect(load, [load, reloadKey]);

  // Opening a specific batch fetches that batch by id, so it opens even when it is not on this page.
  React.useEffect(() => {
    if (!focusBatch) return;
    api
      .get<VoucherBatchesResp>(`/vouchers/batches?batch_id=${encodeURIComponent(focusBatch.id)}`)
      .then((m) => m.batches?.[0] && setOpen(m.batches[0]))
      .catch(setErr);
  }, [focusBatch]);

  const rows = data?.batches ?? null;

  return (
    <div className="space-y-4">
      <ErrorBanner err={err} />
      {!!data?.unbatched_vouchers && (
        <Callout tone="info">
          {data.unbatched_vouchers.toLocaleString()} card{data.unbatched_vouchers === 1 ? " was" : "s were"} printed
          before batches were recorded. They are listed under Vouchers, but cannot be exported as a batch.
        </Callout>
      )}
      <Card>
        {rows === null ? (
          <SkeletonRows rows={4} cols={5} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Layers />}
            title="No batches yet"
            hint="Every time you issue vouchers, the cards form a batch here, ready to export or print again."
          />
        ) : (
          <TableWrap>
            <Table>
              <THead>
                <TR>
                  <TH>Batch</TH>
                  <TH>Package</TH>
                  <TH className="text-right">Cards</TH>
                  <TH>Status</TH>
                  <TH className="hidden md:table-cell">Issued by</TH>
                  <TH className="hidden lg:table-cell">Note</TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((b) => (
                  <TR
                    key={b.batch_id}
                    tabIndex={0}
                    className="cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50"
                    onClick={() => setOpen(b)}
                    onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), setOpen(b))}
                  >
                    <TD>
                      <div className="font-medium">
                        <When iso={b.created_at} />
                      </div>
                      <div className="font-mono text-2xs text-muted-foreground">{b.batch_id.slice(0, 8)}</div>
                    </TD>
                    <TD>{b.package_name ?? "Earlier package version"}</TD>
                    <TD className="text-right tabular">{b.count.toLocaleString()}</TD>
                    <TD>
                      <div className="flex flex-wrap gap-1">
                        <Badge tone="ok">{b.available.toLocaleString()} available</Badge>
                        {b.redeemed > 0 && <Badge tone="neutral">{b.redeemed.toLocaleString()} used</Badge>}
                        {b.expired > 0 && <Badge tone="warn">{b.expired.toLocaleString()} expired</Badge>}
                        {b.cancelled > 0 && <Badge tone="err">{b.cancelled.toLocaleString()} cancelled</Badge>}
                        {b.not_yet_valid > 0 && <Badge tone="info">{b.not_yet_valid.toLocaleString()} not yet valid</Badge>}
                      </div>
                    </TD>
                    <TD className="hidden md:table-cell">{b.issued_by_label ?? "—"}</TD>
                    <TD className="hidden max-w-[16rem] truncate text-muted-foreground lg:table-cell">{b.notes ?? ""}</TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </TableWrap>
        )}
        {rows && rows.length > 0 && (
          <div className="border-t border-border px-4 py-3">
            <Pagination offset={offset} limit={PAGE} shown={rows.length} total={data?.total ?? null} onChange={setOffset} />
          </div>
        )}
      </Card>

      <BatchSheet
        batch={open}
        onOpenChange={(v) => !v && setOpen(null)}
        canRevealCodes={canRevealCodes}
        onViewCards={(id) => {
          setOpen(null);
          onViewCards(id);
        }}
        onExported={load}
        hotelName={hotelName}
      />
    </div>
  );
}

function BatchSheet({
  batch,
  onOpenChange,
  canRevealCodes,
  onViewCards,
  onExported,
  hotelName,
}: {
  batch: VoucherBatch | null;
  onOpenChange: (v: boolean) => void;
  canRevealCodes: boolean;
  onViewCards: (id: string) => void;
  onExported: () => void;
  hotelName?: string;
}) {
  return (
    <Sheet open={batch !== null} onOpenChange={onOpenChange}>
      <SheetContent
        width="lg"
        // Focus the panel itself, not its first focusable element: that is a timestamp with a tooltip, which
        // would open on arrival and swallow the first Escape.
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          (e.currentTarget as HTMLElement).focus();
        }}
      >
        {batch && (
          <BatchDetail
            key={batch.batch_id}
            batch={batch}
            canRevealCodes={canRevealCodes}
            onViewCards={onViewCards}
            onExported={onExported}
            hotelName={hotelName}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

function BatchDetail({
  batch,
  canRevealCodes,
  onViewCards,
  onExported,
  hotelName,
}: {
  batch: VoucherBatch;
  canRevealCodes: boolean;
  onViewCards: (id: string) => void;
  onExported: () => void;
  hotelName?: string;
}) {
  const toast = useToast();
  const [confirming, setConfirming] = React.useState(false);
  const [scope, setScope] = React.useState<"all" | "unused">("all");
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<unknown>(null);
  // Exported codes live in this component only; closing the sheet unmounts it and drops them.
  const [exported, setExported] = React.useState<ExportedBatch | null>(null);

  const planned = scope === "unused" ? batch.unused : batch.count;
  const packageName = batch.package_name ?? "Internet access";
  const validity = validityWords(batch.redemption_valid_from, batch.redemption_valid_until);

  async function doExport({ reason, password }: { reason: string; password: string }) {
    const bad = reasonProblem(reason);
    if (bad) return setErr(new Error(bad));
    setBusy(true);
    setErr(null);
    try {
      const body: Record<string, unknown> = { password, reason, batch_id: batch.batch_id };
      if (scope === "unused") body.state = "UNUSED";
      const out = await api.post<ExportedBatch>("/voucher-codes/export", body);
      setExported(out);
      setConfirming(false);
      toast.success(`${out.count} code${out.count === 1 ? "" : "s"} exported`, "The export was recorded with your name and reason.");
      onExported();
    } catch (e) {
      setErr(stepUpError(e, "No codes were exported."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <SheetHeader
        eyebrow="Batch"
        icon={<Layers />}
        title={batchLabel(batch)}
        description={`${batch.count.toLocaleString()} cards for ${packageName}`}
      />
      <SheetBody>
        <MetricStrip
          items={[
            { label: "Available", value: batch.available.toLocaleString(), tone: "ok" },
            { label: "Used", value: batch.redeemed.toLocaleString() },
            { label: "Expired unused", value: batch.expired.toLocaleString(), tone: batch.expired ? "warn" : undefined },
            { label: "Cancelled", value: batch.cancelled.toLocaleString(), tone: batch.cancelled ? "err" : undefined },
          ]}
        />
        <SheetSection title="Details">
          <KeyValueGrid
            items={[
              { label: "Package", value: packageName },
              { label: "Validity", value: validity },
              { label: "Issued", value: <When iso={batch.created_at} /> },
              { label: "Issued by", value: batch.issued_by_label ?? (batch.issued_by ? "An operator account" : null) },
              { label: "Not yet valid", value: batch.not_yet_valid.toLocaleString() },
              { label: "Batch reference", value: <MonoId value={batch.batch_id} title="Batch reference" /> },
              { label: "Note", value: batch.notes, wide: true },
            ]}
          />
        </SheetSection>
        {exported && (
          <SheetSection title={`Exported codes (${exported.count.toLocaleString()})`}>
            <CodesHeld
              codes={exported.vouchers}
              packageName={packageName}
              validity={validity}
              batchId={exported.batch_id}
              defaultHeading={hotelName}
              warning={
                <>
                  This export was recorded with your name, your reason and the number of codes. The codes disappear
                  when you close this panel.
                </>
              }
            />
          </SheetSection>
        )}
      </SheetBody>
      <SheetFooter>
        <Button variant="secondary" size="sm" onClick={() => onViewCards(batch.batch_id)}>
          <List /> View cards
        </Button>
        {canRevealCodes && (
          <Button
            size="sm"
            onClick={() => {
              setErr(null);
              setScope("all");
              setConfirming(true);
            }}
          >
            <Download /> Export codes
          </Button>
        )}
      </SheetFooter>

      <ConfirmDialog
        open={confirming}
        onOpenChange={(v) => !v && setConfirming(false)}
        title="Export the codes of this batch"
        description={`${planned.toLocaleString()} code${planned === 1 ? "" : "s"} will be recovered and shown to you. One record is kept naming you, your reason and that number.`}
        confirmLabel={`Export ${planned.toLocaleString()} code${planned === 1 ? "" : "s"}`}
        busy={busy}
        error={err}
        requireReason
        reasonLabel="Reason (recorded)"
        reasonPlaceholder="Reprinting the conference batch"
        requirePassword
        onConfirm={doExport}
      >
        {batch.unused > 0 && batch.unused < batch.count && (
          <Segmented
            label="Which cards"
            value={scope}
            onChange={setScope}
            options={[
              { value: "all", label: "Whole batch", count: batch.count },
              { value: "unused", label: "Not used yet", count: batch.unused },
            ]}
          />
        )}
        {scope === "unused" && batch.expired > 0 && (
          <p className="text-xs text-muted-foreground">
            “Not used yet” includes {batch.expired.toLocaleString()} expired card{batch.expired === 1 ? "" : "s"}, which
            sign-in will refuse.
          </p>
        )}
      </ConfirmDialog>
    </>
  );
}
