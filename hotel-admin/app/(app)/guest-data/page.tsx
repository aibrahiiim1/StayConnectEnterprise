"use client";

// Only site_admin and the IT manager reach this page; every export and erasure asks for the password and a reason
// and is recorded by id, never by name. Erasure anonymises and is refused while the guest is staying or online.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { api, ApiError, BACKGROUND_HEADER } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button, LinkButton } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { RowActions } from "@/components/ui/row-actions";
import { HistoryList } from "@/components/ui/history";
import { DetailsDrawer } from "@/components/ui/details-drawer";
import { HelpSection } from "@/components/help";
import { ConfirmDialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { DescriptionList, ResultsCount, SEARCH_FIELD_PROPS } from "@/components/ui/data";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";
import { Download, Eraser, Search } from "lucide-react";
import { LIVE_REASONS, eraseConsequences, exportDescription } from "@/lib/guest-data-words";
import { ledgerHeadline, type LedgerStatus } from "@/lib/ledger-words";

type Kind = "account" | "principal" | "stay" | "stay_guest";
type Match = {
  kind: Kind; id: string; label: string; detail: string | null; status: string | null; last_activity: string | null;
};
type Erasure = {
  erasure_ref: string; subject_kind: Kind; erased_by: string; reason: string; erased_at: string;
  counts: Record<string, number>;
};

const KIND_LABELS: Record<Kind, string> = {
  account: "Client account",
  principal: "Portal sign-in identity",
  stay: "Hotel stay",
  stay_guest: "Guest on a shared stay",
};

function saveJSON(doc: unknown) {
  const blob = new Blob([JSON.stringify(doc, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `guest-data-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "")}.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export default function GuestDataPage() {
  const toast = useToast();
  const [q, setQ] = useState("");
  const [matches, setMatches] = useState<Match[] | null>(null);
  const [matchesCut, setMatchesCut] = useState(false);
  const [erasuresCut, setErasuresCut] = useState(false);
  const [ledger, setLedger] = useState<LedgerStatus | null>(null);
  const [searching, setSearching] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [erasures, setErasures] = useState<Erasure[] | null>(null);

  const [exporting, setExporting] = useState<Match | null>(null);
  const [erasing, setErasing] = useState<Match | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [done, setDone] = useState<{ ref: string; counts: Record<string, number> } | null>(null);
  const [record, setRecord] = useState<Erasure | null>(null);

  const loadErasures = useCallback(async () => {
    try {
      const r = await api.get<{ erasures: Erasure[]; truncated?: boolean }>("/guest-data-rights/erasures");
      setErasures(r.erasures ?? []);
      setErasuresCut(r.truncated === true);
    } catch { setErasures([]); }
  }, []);
  useEffect(() => { void loadErasures(); }, [loadErasures]);
  // Erasure needs both copies of the erasure ledger (Backups); say so before the request.
  useEffect(() => {
    void (async () => {
      try { setLedger(await api.get<LedgerStatus>("/backups/ledger", { [BACKGROUND_HEADER]: "1" })); } catch { /* no banner */ }
    })();
  }, []);

  async function search() {
    const term = q.trim();
    if (term.length < 2) return;
    setSearching(true); setErr(null);
    try {
      const r = await api.get<{ matches: Match[]; truncated?: boolean }>("/guest-data-rights/search", { "X-Guest-Data-Search": encodeURIComponent(term) });
      setMatches(r.matches ?? []);
      setMatchesCut(r.truncated === true);
    } catch (e) { setErr(errMsg(e)); } finally { setSearching(false); }
  }

  async function doExport({ reason, password }: { reason: string; password: string }) {
    if (!exporting) return;
    setBusy(true); setActionErr(null);
    try {
      const doc = await api.post<unknown>("/guest-data-rights/export", { kind: exporting.kind, id: exporting.id, reason, password });
      saveJSON(doc);
      setExporting(null);
      setMatches(null); setMatchesCut(false); setQ("");
      toast.success("Export downloaded", "Hand it to the guest only after checking who they are.");
    } catch (e) { setActionErr(errMsg(e)); } finally { setBusy(false); }
  }

  async function doErase({ reason, password }: { reason: string; password: string }) {
    if (!erasing) return;
    setBusy(true); setActionErr(null);
    try {
      const r = await api.post<{ erasure_ref: string; counts: Record<string, number> }>(
        "/guest-data-rights/erase", { kind: erasing.kind, id: erasing.id, reason, password });
      setErasing(null);
      setDone({ ref: r.erasure_ref, counts: r.counts });
      setMatches(null); setMatchesCut(false); setQ("");
      await loadErasures();
    } catch (e) {
      if (e instanceof ApiError && e.code === "guest_data_live") {
        const live: string[] = Array.isArray(e.body?.live) ? e.body.live : [];
        setActionErr("Nothing was erased: " + live.map((x) => LIVE_REASONS[x] ?? x).join("; ") + ".");
      } else setActionErr(errMsg(e));
    } finally { setBusy(false); }
  }

  const countWords = (counts: Record<string, number>) =>
    Object.entries(counts).filter(([, n]) => n > 0).map(([k, n]) => `${n} ${k.replace(/_/g, " ")}`).join(", ");

  return (
    <PageShell>
      <PageHeader
        title="Guest data requests"
        description="Give a guest a copy of their data, or erase it, when they ask."
        help={
          <>
            <HelpSection title="Records no rule can attribute to one person">
              <p>
                The internet access used through a room, a voucher or a staff-issued account, and a device used with
                someone else, is shared: it is never exported or erased here. If a guest asks for the usage of their own
                phone, open an <strong>assisted request</strong>: you verify the requester, review the exact records,
                and only records established as theirs can be handed over or anonymised.
              </p>
            </HelpSection>
            <HelpSection title="Request register">
              <p>Keep track of each request and its deadline in the request register.</p>
            </HelpSection>
            <HelpSection title="Erasures">
              <p>Erasure anonymises the records and is refused while the guest is staying or online. Each erasure has a reference to give the guest as proof.</p>
            </HelpSection>
          </>
        }
        actions={
          <>
            <LinkButton href="/guest-data/register" variant="secondary">Request register</LinkButton>
            <LinkButton href="/guest-data/assisted" variant="secondary">Assisted request</LinkButton>
          </>
        }
      />

      <Callout tone="warning" title="Check the guest’s identity first">
        Before you hand over an export or erase anything. Do not write the guest&rsquo;s name, address, phone, room or
        reservation in the reason: it is kept after the guest is erased, so a reason that names them is refused.
      </Callout>

      {ledger?.state && ledger.erasure_available === false && (
        <Callout tone="warning" title="Erasure is paused">
          {ledgerHeadline(ledger).detail}{" "}
          <Link className="underline" href="/backups">Open Backups</Link>. Exporting a guest&rsquo;s data still works, and guest internet access is not affected.
        </Callout>
      )}

      {done && (
        <Callout tone="success">
          Erased. Reference <strong className="font-mono">{done.ref}</strong>: give it to the guest as proof. {""}
          {countWords(done.counts)}.
        </Callout>
      )}

      <SectionCard
        flush
        title="Find the guest"
        description="Email, phone, name, client account, room, reservation number or device MAC address."
      >
        <div className="flex flex-wrap gap-2 border-b border-border px-4 py-3" role="search">
          <Input
            {...SEARCH_FIELD_PROPS}
            name="guest-data-lookup"
            aria-label="Search"
            className="min-w-0 flex-1"
            value={q}
            maxLength={200}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); void search(); } }}
            placeholder="guest@example.com, 412, Smith, aa:bb:cc:dd:ee:ff"
          />
          <Button variant="primary" disabled={searching || q.trim().length < 2} onClick={() => void search()}>
            <Search /> {searching ? "Searching…" : "Search"}
          </Button>
        </div>
        <ResultsCount count={matches?.length} noun="record" />
        {(err || matchesCut) && (
          <div className="space-y-3 px-4 pt-3">
            <ErrorBanner err={err} className="mb-0" />
            {matchesCut && (
              <Callout tone="warning">
                Only the first 50 records are shown. Search for something more specific (the full email, the phone
                number or the reservation number).
              </Callout>
            )}
          </div>
        )}
        {matches === null ? (
          <p className="px-4 py-6 text-center text-sm text-muted-foreground">Search to list the guest&rsquo;s records.</p>
        ) : matches.length === 0 ? (
          <EmptyState size="sm" title="No guest found" hint="Records already erased or anonymised by retention are not shown." />
        ) : (
          <Table label="Matching records">
            <THead>
              <TR>
                <TH>Record</TH>
                <TH>Found by</TH>
                <TH className="hidden sm:table-cell">Status</TH>
                <TH className="hidden md:table-cell">Last activity</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {matches.map((m) => (
                <TR key={`${m.kind}-${m.id}`}>
                  <TD>
                    <div className="text-sm">{KIND_LABELS[m.kind]}</div>
                    {m.detail && <div className="text-caption text-muted-foreground">{m.detail}</div>}
                  </TD>
                  <TD className="break-all">{m.label}</TD>
                  <TD nowrap className="hidden sm:table-cell">{m.status ? <Badge tone="neutral">{m.status}</Badge> : null}</TD>
                  <TD nowrap muted className="hidden md:table-cell">{m.last_activity ? formatDate(m.last_activity) : "—"}</TD>
                  <TD className="w-0">
                    <RowActions
                      subject={`${KIND_LABELS[m.kind]} ${m.label}`}
                      primary={{ label: "Export", icon: <Download />, onSelect: () => { setActionErr(null); setExporting(m); } }}
                      items={[{
                        label: "Erase…",
                        icon: <Eraser />,
                        danger: true,
                        onSelect: () => { setActionErr(null); setDone(null); setErasing(m); },
                      }]}
                    />
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </SectionCard>

      <SectionCard flush title="Erasures" description="Every erasure on this site. They hold no guest data.">
        {erasuresCut && (
          <div className="px-4 pt-3"><Callout tone="neutral">Only the 200 most recent erasures are shown. Older ones are kept.</Callout></div>
        )}
        {erasures !== null && (
          <HistoryList
            label="Erasures"
            emptyLabel="No guest has been erased."
            items={erasures.map((x) => ({
              id: x.erasure_ref,
              title: <span className="font-mono">{x.erasure_ref}</span>,
              actor: x.erased_by,
              at: x.erased_at,
              summary: x.reason,
            }))}
            selectedId={record?.erasure_ref ?? null}
            onSelect={(it) => setRecord(erasures.find((x) => x.erasure_ref === it.id) ?? null)}
          />
        )}
      </SectionCard>

      <DetailsDrawer
        open={record !== null}
        onOpenChange={(v) => !v && setRecord(null)}
        eyebrow="Erasure"
        title={record ? <span className="font-mono">{record.erasure_ref}</span> : ""}
        description={record ? formatDate(record.erased_at) : undefined}
      >
        {record && (
          <DescriptionList
            items={[
              { label: "Record type", value: KIND_LABELS[record.subject_kind] ?? record.subject_kind },
              { label: "Erased by", value: record.erased_by },
              { label: "When", value: formatDate(record.erased_at) },
              { label: "Reason", value: record.reason },
              { label: "What was anonymised", value: countWords(record.counts || {}) || "—" },
            ]}
          />
        )}
      </DetailsDrawer>

      <ConfirmDialog
        open={exporting !== null}
        onOpenChange={(v) => { if (!v) { setExporting(null); setActionErr(null); } }}
        title="Export this guest’s data?"
        description={exportDescription(exporting?.kind)}
        confirmLabel="Download export"
        busy={busy}
        error={actionErr}
        requireReason
        reasonMinLength={4}
        reasonPlaceholder="e.g. written request received at reception"
        requirePassword
        onConfirm={doExport}
      />

      <ConfirmDialog
        open={erasing !== null}
        onOpenChange={(v) => { if (!v) { setErasing(null); setActionErr(null); } }}
        title="Erase this guest?"
        confirmLabel="Erase guest"
        confirmVariant="danger"
        busy={busy}
        error={actionErr}
        confirmText="ERASE"
        consequences={eraseConsequences(erasing?.kind)}
        requireReason
        reasonMinLength={4}
        reasonPlaceholder="e.g. written erasure request received at reception"
        requirePassword
        onConfirm={doErase}
      />
    </PageShell>
  );
}
