"use client";

// Code-format changes require a preview, reason, and confirmation before saving.

import * as React from "react";
import { KeyRound, Pencil, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { ChangeDiff, HistoryList } from "@/components/ui/history";
import { RowActions } from "@/components/ui/row-actions";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Field, Input, Select } from "@/components/ui/input";
import { DescriptionList, KeyValueGrid, OptionCard } from "@/components/ui/data";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
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
  const [openChange, setOpenChange] = React.useState<{ c: FormatChange; i: number } | null>(null);

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
    <div className="grid gap-5 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <div className="min-w-0">
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

      <Card className="overflow-hidden">
        <CardHeader>
          <div className="min-w-0">
            <CardTitle>Format history</CardTitle>
            <CardDescription>Every change, who made it and why.</CardDescription>
          </div>
        </CardHeader>
        {changesErr ? (
          <CardBody>
            <ErrorBanner err={changesErr} />
          </CardBody>
        ) : changes === null ? (
          <CardBody>
            <Skeleton className="h-16" />
          </CardBody>
        ) : (
          <HistoryList
            label="Format history"
            emptyLabel="The format has never been changed; the defaults are in use."
            onSelect={(it) => {
              const i = Number(it.id);
              setOpenChange({ c: changes[i], i });
            }}
            selectedId={openChange ? String(openChange.i) : null}
            items={changes.map((c, i) => ({
              id: String(i),
              title: c.old_code_mode && c.old_code_length
                ? `${formatWords(c.old_code_mode, c.old_code_length)} → ${formatWords(c.new_code_mode, c.new_code_length)}`
                : `Set to ${formatWords(c.new_code_mode, c.new_code_length)}`,
              actor: c.changed_by,
              at: c.changed_at,
              summary: c.change_reason ? `“${c.change_reason}”` : undefined,
              tone: "info" as const,
            }))}
          />
        )}
      </Card>

      <DetailsDrawer
        open={openChange !== null}
        onOpenChange={(v) => !v && setOpenChange(null)}
        eyebrow="Code format change"
        title={openChange ? `Set to ${formatWords(openChange.c.new_code_mode, openChange.c.new_code_length)}` : ""}
      >
        {openChange && (
          <>
            <DetailsSection title="Change">
              <ChangeDiff
                changes={[
                  {
                    field: "Characters",
                    before: openChange.c.old_code_mode ? (openChange.c.old_code_mode === "numbers" ? "Digits only" : "Letters and digits") : undefined,
                    after: openChange.c.new_code_mode === "numbers" ? "Digits only" : "Letters and digits",
                  },
                  {
                    field: "Length",
                    before: openChange.c.old_code_length ? `${openChange.c.old_code_length} characters` : undefined,
                    after: `${openChange.c.new_code_length} characters`,
                  },
                ]}
              />
            </DetailsSection>
            <DetailsSection title="Record">
              <DescriptionList
                items={[
                  { label: "Changed by", value: openChange.c.changed_by || "—" },
                  { label: "When", value: <When iso={openChange.c.changed_at} /> },
                  { label: "Reason", value: openChange.c.change_reason || "—" },
                ]}
              />
            </DetailsSection>
          </>
        )}
      </DetailsDrawer>

      {canEditFormat && (
        <Card className="overflow-hidden lg:col-span-2">
          <CardHeader>
            <div className="min-w-0">
              <CardTitle>Code keys</CardTitle>
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
            <EmptyState size="sm" icon={<KeyRound />} title="No key yet" hint="The first batch you issue creates one." />
          ) : (
              <Table label="Code keys">
                <THead>
                  <TR>
                    <TH>Key</TH>
                    <TH>State</TH>
                    <TH numeric>Cards</TH>
                    <TH numeric>Still unused</TH>
                    <TH><span className="sr-only">Actions</span></TH>
                  </TR>
                </THead>
                <TBody>
                  {gens.map((g) => (
                    <TR key={g.id}>
                      <TD nowrap>Generation {g.generation_no}</TD>
                      <TD>
                        {g.active ? (
                          <StatusBadge tone="ok">In use</StatusBadge>
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
                      <TD numeric>{g.vouchers.toLocaleString()}</TD>
                      <TD numeric>{g.unused_vouchers.toLocaleString()}</TD>
                      <TD className="w-0">
                        {g.active && (
                          <RowActions
                            subject={`generation ${g.generation_no}`}
                            items={[{
                              label: "Retire key…",
                              danger: true,
                              onSelect: () => {
                                setErr(null);
                                setRetiring(g);
                              },
                            }]}
                          />
                        )}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
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
            <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="Clients find mixed codes hard to type" />
          </Field>
        </>
      )}
    </DialogForm>
  );
}
