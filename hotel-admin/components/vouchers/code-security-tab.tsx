"use client";

// CODE SECURITY — what a code looks like, how that has changed, and the keys codes are indexed under.
//
// The format used to save the moment a dropdown changed: no preview, no reason, no confirmation, and no way
// to see what it had been before. It is now an explicit change -- choose, see what new cards will look like,
// give a reason, confirm -- and the history the server has always kept is shown beside it.

import * as React from "react";
import { History, KeyRound, Pencil, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Field, Input, Select } from "@/components/ui/input";
import { KeyValueGrid, OptionCard, Timeline } from "@/components/ui/data";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { Table, TableWrap, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import {
  formatWords,
  sampleCode,
  type FormatChange,
  type KeyGeneration,
  type VoucherCodeFormat,
} from "@/lib/api/vouchers";
import { reasonProblem, stepUpError, When } from "./shared";

type Mode = "numbers" | "mixed";

export function CodeSecurityTab({ canEditFormat }: { canEditFormat: boolean }) {
  const toast = useToast();
  const [format, setFormat] = React.useState<VoucherCodeFormat | null>(null);
  const [formatErr, setFormatErr] = React.useState<unknown>(null);
  const [changes, setChanges] = React.useState<FormatChange[] | null>(null);
  const [changesErr, setChangesErr] = React.useState<unknown>(null);
  const [gens, setGens] = React.useState<KeyGeneration[] | null>(null);
  const [gensErr, setGensErr] = React.useState<unknown>(null);
  const [editing, setEditing] = React.useState(false);
  const [retiring, setRetiring] = React.useState<KeyGeneration | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<unknown>(null);

  const loadFormat = React.useCallback(() => {
    setFormatErr(null);
    api.get<VoucherCodeFormat>("/voucher-code-settings/").then(setFormat).catch(setFormatErr);
    api
      .get<{ changes?: FormatChange[] }>("/voucher-code-settings/changes")
      .then((m) => setChanges(m.changes ?? []))
      .catch(setChangesErr);
  }, []);
  const loadGens = React.useCallback(() => {
    if (!canEditFormat) return;
    setGensErr(null);
    api
      .get<{ generations?: KeyGeneration[] }>("/voucher-code-settings/key-generations")
      .then((m) => setGens(m.generations ?? []))
      .catch(setGensErr);
  }, [canEditFormat]);

  React.useEffect(loadFormat, [loadFormat]);
  React.useEffect(loadGens, [loadGens]);

  async function retire({ reason, password }: { reason: string; password: string }) {
    if (!retiring) return;
    const bad = reasonProblem(reason);
    if (bad) return setErr(new Error(bad));
    setBusy(true);
    setErr(null);
    try {
      await api.post(`/voucher-code-settings/key-generations/${retiring.id}/supersede`, { password, reason });
      toast.success(`Key generation ${retiring.generation_no} retired`, "The next batch you issue uses a new key.");
      setRetiring(null);
      loadGens();
    } catch (e) {
      setErr(stepUpError(e, "The key was not retired."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle>Code format</CardTitle>
            <CardDescription>Applies to the next batch you issue. Cards already printed keep working.</CardDescription>
          </div>
          {canEditFormat && format && (
            <Button size="sm" variant="secondary" onClick={() => setEditing(true)}>
              <Pencil /> Change format
            </Button>
          )}
        </CardHeader>
        <CardBody className="space-y-4">
          {formatErr ? (
            <div className="space-y-2">
              <ErrorBanner err={formatErr} />
              <Button size="sm" variant="secondary" onClick={loadFormat}>
                <RefreshCw /> Try again
              </Button>
            </div>
          ) : !format ? (
            <Skeleton className="h-16" />
          ) : (
            <>
              <KeyValueGrid
                items={[
                  { label: "Characters", value: format.code_mode === "numbers" ? "Digits only" : "Letters and digits" },
                  { label: "Length", value: `${format.code_length} characters` },
                  {
                    label: "New cards look like",
                    value: <span className="font-mono text-base tracking-widest">{sampleCode(format.code_mode, format.code_length)}</span>,
                  },
                  {
                    label: "Chosen",
                    value: format.config_version === 0 ? "Not yet: the defaults are in use" : <When iso={format.updated_at} />,
                  },
                ]}
              />
              {!canEditFormat && (
                <p className="text-xs text-muted-foreground">Your role can see the format but not change it.</p>
              )}
            </>
          )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle>
              <span className="inline-flex items-center gap-2">
                <History className="size-4" aria-hidden /> Format history
              </span>
            </CardTitle>
            <CardDescription>Every change, who made it and why.</CardDescription>
          </div>
        </CardHeader>
        <CardBody>
          {changesErr ? (
            <ErrorBanner err={changesErr} />
          ) : changes === null ? (
            <Skeleton className="h-16" />
          ) : (
            <Timeline
              emptyLabel="The format has never been changed; the defaults are in use."
              items={changes.map((c, i) => ({
                key: `${c.changed_at}-${i}`,
                title:
                  c.old_code_mode && c.old_code_length
                    ? `${formatWords(c.old_code_mode, c.old_code_length)} → ${formatWords(c.new_code_mode, c.new_code_length)}`
                    : `Set to ${formatWords(c.new_code_mode, c.new_code_length)}`,
                when: <When iso={c.changed_at} />,
                body: [c.changed_by, c.change_reason && `“${c.change_reason}”`].filter(Boolean).join(" · "),
                tone: "info" as const,
              }))}
            />
          )}
        </CardBody>
      </Card>

      {canEditFormat && (
        <Card className="lg:col-span-2">
          <CardHeader>
            <div className="space-y-0.5">
              <CardTitle>
                <span className="inline-flex items-center gap-2">
                  <KeyRound className="size-4" aria-hidden /> Code keys
                </span>
              </CardTitle>
              <CardDescription>Retiring a key means new batches use a fresh one; cards already printed keep working.</CardDescription>
            </div>
          </CardHeader>
          {gensErr ? (
            <CardBody>
              <ErrorBanner err={gensErr} />
            </CardBody>
          ) : gens === null ? (
            <SkeletonRows rows={2} cols={4} />
          ) : gens.length === 0 ? (
            <EmptyState icon={<KeyRound />} title="No key yet" hint="The first batch you issue creates one." />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>Key</TH>
                    <TH>State</TH>
                    <TH className="text-right">Cards</TH>
                    <TH className="text-right">Still unused</TH>
                    <TH />
                  </TR>
                </THead>
                <TBody>
                  {gens.map((g) => (
                    <TR key={g.id}>
                      <TD>Generation {g.generation_no}</TD>
                      <TD>
                        {g.active ? (
                          <Badge tone="ok" dot>
                            In use
                          </Badge>
                        ) : (
                          <span className="space-y-0.5">
                            <Badge tone="neutral">Retired</Badge>{" "}
                            <When iso={g.superseded_at} className="text-xs text-muted-foreground" />
                            {g.supersede_reason && (
                              <span className="block text-xs text-muted-foreground">“{g.supersede_reason}”</span>
                            )}
                          </span>
                        )}
                      </TD>
                      <TD className="text-right tabular">{g.vouchers.toLocaleString()}</TD>
                      <TD className="text-right tabular">{g.unused_vouchers.toLocaleString()}</TD>
                      <TD className="text-right">
                        {g.active && (
                          <Button
                            size="xs"
                            variant="danger"
                            onClick={() => {
                              setErr(null);
                              setRetiring(g);
                            }}
                          >
                            Retire
                          </Button>
                        )}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </Card>
      )}

      {format && (
        <FormatDialog
          open={editing}
          onOpenChange={setEditing}
          current={format}
          onSaved={(f) => {
            setFormat(f);
            loadFormat();
          }}
        />
      )}

      <ConfirmDialog
        open={retiring !== null}
        onOpenChange={(v) => !v && setRetiring(null)}
        title={retiring ? `Retire key generation ${retiring.generation_no}` : "Retire key"}
        description="New batches will be issued under a fresh key. This cannot be undone."
        confirmLabel="Retire this key"
        confirmVariant="danger"
        busy={busy}
        error={err}
        requireReason
        reasonLabel="Reason (recorded)"
        reasonPlaceholder="Scheduled rotation"
        requirePassword
        onConfirm={retire}
      >
        {retiring && (
          <p className="text-sm text-muted-foreground">
            The <strong>{retiring.unused_vouchers.toLocaleString()} unused cards</strong> made with this key keep
            working: each card stays tied to the key that made it, so nothing in circulation stops.
          </p>
        )}
      </ConfirmDialog>
    </div>
  );
}

function FormatDialog({
  open,
  onOpenChange,
  current,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  current: VoucherCodeFormat;
  onSaved: (f: VoucherCodeFormat) => void;
}) {
  const toast = useToast();
  const [mode, setMode] = React.useState<Mode>(current.code_mode);
  const [length, setLength] = React.useState<number>(current.code_length);
  const [reason, setReason] = React.useState("");
  const [review, setReview] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<unknown>(null);

  React.useEffect(() => {
    if (open) {
      setMode(current.code_mode);
      setLength(current.code_length);
      setReason("");
      setReview(false);
      setErr(null);
    }
  }, [open, current]);

  const unchanged = mode === current.code_mode && length === current.code_length;
  const reasonErr = reason === "" ? null : reasonProblem(reason);

  async function submit() {
    if (!review) {
      const bad = reasonProblem(reason);
      if (bad) return setErr(new Error(bad));
      setErr(null);
      setReview(true);
      return;
    }
    setBusy(true);
    setErr(null);
    try {
      const out = await api.put<VoucherCodeFormat & { notice?: string }>("/voucher-code-settings/", {
        code_mode: mode,
        code_length: length,
        reason: reason.trim(),
      });
      toast.success("Code format changed", out.notice ?? "It applies to the next batch you issue.");
      onSaved(out);
      onOpenChange(false);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      title={review ? "Confirm the new code format" : "Change the code format"}
      description="Only cards issued after this change use the new format. Cards already printed are unaffected."
      submitLabel={review ? "Apply to new batches" : "Review change"}
      busy={busy}
      error={err}
      disabled={unchanged || (!review && reason.trim().length < 4)}
      extraFooter={
        review ? (
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => setReview(false)}>
            Back
          </Button>
        ) : undefined
      }
      onSubmit={submit}
    >
      {review ? (
        <KeyValueGrid
          items={[
            { label: "Now", value: `${formatWords(current.code_mode, current.code_length)} · ${sampleCode(current.code_mode, current.code_length)}` },
            { label: "After", value: `${formatWords(mode, length)} · ${sampleCode(mode, length)}` },
            { label: "Reason", value: reason.trim(), wide: true },
          ]}
        />
      ) : (
        <>
          <div role="radiogroup" aria-label="Characters" className="grid gap-3 sm:grid-cols-2">
            <OptionCard
              name="code-mode"
              value="numbers"
              checked={mode === "numbers"}
              onChange={() => setMode("numbers")}
              title="Digits only"
              description="Easiest on a phone keypad."
            />
            <OptionCard
              name="code-mode"
              value="mixed"
              checked={mode === "mixed"}
              onChange={() => setMode("mixed")}
              title="Letters and digits"
              description="Harder to guess at the same length."
            />
          </div>
          <Field label="Length">
            <Select value={length} onChange={(e) => setLength(Number(e.target.value))}>
              {[6, 7, 8].map((n) => (
                <option key={n} value={n}>
                  {n} characters
                </option>
              ))}
            </Select>
          </Field>
          <div className="rounded-md border border-border bg-surface px-3 py-2 text-sm">
            New cards will look like:{" "}
            <span className="font-mono text-base font-semibold tracking-widest" data-testid="format-preview">
              {sampleCode(mode, length)}
            </span>
            <span className="block text-xs text-muted-foreground">
              An example of the shape only. Easily confused characters (0/O, 1/I/L, 5/S) are never used.
            </span>
          </div>
          {unchanged && <Callout tone="neutral">This is the current format. Choose something different to change it.</Callout>}
          <Field label="Reason for the change" error={reasonErr ?? undefined} hint="Recorded in the format history." required>
            <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="Guests find mixed codes hard to type" />
          </Field>
        </>
      )}
    </DialogForm>
  );
}
