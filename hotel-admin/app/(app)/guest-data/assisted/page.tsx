"use client";

// Assisted requests cover shared access (room credentials, vouchers, staff accounts) where a device address or room
// number does not identify a person.
// Only site_admin and the IT manager reach this page; the database decides what is selectable and whether anything
// changed.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button, LinkButton } from "@/components/ui/button";
import { Input, Textarea, Select, Field } from "@/components/ui/input";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { DescriptionList } from "@/components/ui/data";
import { HistoryList } from "@/components/ui/history";
import { ConfirmDialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";
import { ArrowLeft, Download, Eraser, Plus, Search, X } from "lucide-react";
import {
  ASSISTED_ERASE_CONSEQUENCES, ASSISTED_EXPORT_NOTE, BLOCK_WORDS, CLASS_WORDS, KIND_WORDS, METHOD_OPTIONS,
  PURPOSE_WORDS, REASON_WORDS, REFUSAL_WORDS, STATUS_TONE, describeDetail, isGroupAddress, isPrivateAddress,
  parseAddresses, type RecordClass,
} from "@/lib/guest-data-assisted-words";

type AnchorKind = "account" | "principal" | "stay" | "stay_guest";
type Match = { kind: AnchorKind; id: string; label: string; detail: string | null; status: string | null };
type Anchor = { kind: AnchorKind; id: string; label?: string | null };
type Summary = {
  id: string; ref: string; purpose: "ACCESS" | "ERASURE"; status: "OPEN" | "COMPLETED" | "CANCELLED";
  range_from: string; range_to: string; requester: string; created_by: string; created_at: string;
  closed_at: string | null; closed_by: string | null; result: Record<string, unknown> | null;
};
type Detail = Summary & {
  addresses: string[] | null; verification_method: string; evidence_note: string | null; reason: string;
  statements: { identity_verified: boolean; device_shown: boolean; sole_use: boolean; private_address_confirmed: boolean };
  anchors: Anchor[]; events: { at: string; by: string; action: string; detail: Record<string, unknown> }[];
};
type Rec = {
  kind: string; id: string; at: string; class: RecordClass; reason: string; erase_block: string | null;
  selectable: boolean; detail: Record<string, unknown> | null;
};
type Review = { signature: string; by_class: Partial<Record<RecordClass, number>>; records: Rec[] };

const ANCHOR_LABELS: Record<AnchorKind, string> = {
  account: "Client account", principal: "Portal sign-in identity", stay: "Hotel stay", stay_guest: "Guest on a shared stay",
};
const METHOD_LABEL = Object.fromEntries(METHOD_OPTIONS.map((m) => [m.value, m.label]));
const statusWord = (s: string) => s.charAt(0) + s.slice(1).toLowerCase();
const toneOf = (s: string) => {
  const t = STATUS_TONE[s] ?? "neutral";
  return t === "neutral" ? "default" : t;
};

function saveJSON(doc: unknown, prefix: string) {
  const blob = new Blob([JSON.stringify(doc, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${prefix}-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "")}.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// A datetime-local value is the browser's wall clock; the server wants an instant.
const localInput = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);

export default function AssistedGuestDataPage() {
  const toast = useToast();
  const [list, setList] = useState<Summary[] | null>(null);
  const [listErr, setListErr] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [caseId, setCaseId] = useState<string | null>(null);

  const loadList = useCallback(async () => {
    try {
      const r = await api.get<{ requests: Summary[] }>("/guest-data-rights/assisted");
      setList(r.requests ?? []); setListErr(null);
    } catch (e) { setList([]); setListErr(errMsg(e)); }
  }, []);
  useEffect(() => { void loadList(); }, [loadList]);

  return (
    <PageShell>
      <LinkButton href="/guest-data" variant="ghost" size="sm" className="-ms-2.5 self-start">
        <ArrowLeft className="rtl:rotate-180" /> Guest data requests
      </LinkButton>
      <PageHeader
        title="Assisted guest data request"
        description="Hand over or anonymise one person’s device usage when no rule can tie it to them."
        actions={!caseId && !creating && (
          <Button variant="primary" onClick={() => setCreating(true)}><Plus /> New request</Button>
        )}
      />

      <Callout tone="warning" title="A device address does not identify a person">
        Verify the requester yourself and review the exact records before you hand anything over or anonymise it.
        Records that may belong to someone else are shown but can never be selected.
      </Callout>

      {caseId ? (
        <CasePanel id={caseId} onClose={() => { setCaseId(null); void loadList(); }} onChanged={loadList} />
      ) : (
        <>
          {creating && (
            <NewRequest
              onCancel={() => setCreating(false)}
              onOpened={(id) => { setCreating(false); setCaseId(id); void loadList(); toast.success("Request opened", "Review its records next."); }}
            />
          )}

          <SectionCard
            flush
            title="Requests"
            description="Closed requests keep who, when, how it was verified and how many records — no device address and no notes."
          >
            {listErr && <div className="px-4 pt-3"><ErrorBanner err={listErr} className="mb-0" /></div>}
            {list !== null && list.length === 0 && !listErr && <EmptyState size="sm" title="No assisted request yet" />}
            {list !== null && list.length > 0 && (
              <Table label="Assisted requests">
                <THead>
                  <TR>
                    <TH>Reference</TH>
                    <TH>Purpose</TH>
                    <TH>Status</TH>
                    <TH className="hidden lg:table-cell">Range</TH>
                    <TH className="hidden md:table-cell">Opened</TH>
                    <TH pinEnd><span className="sr-only">Actions</span></TH>
                  </TR>
                </THead>
                <TBody>
                  {list.map((x) => (
                    <TR key={x.id} onClick={() => setCaseId(x.id)}>
                      <TD nowrap className="font-mono text-xs">{x.ref}</TD>
                      <TD>{PURPOSE_WORDS[x.purpose] ?? x.purpose}</TD>
                      <TD nowrap><StatusBadge tone={toneOf(x.status)}>{statusWord(x.status)}</StatusBadge></TD>
                      <TD nowrap muted className="hidden lg:table-cell">
                        <div>{formatDate(x.range_from)} –</div>
                        <div>{formatDate(x.range_to)}</div>
                      </TD>
                      <TD nowrap muted className="hidden max-w-[14rem] md:table-cell">
                        <div>{formatDate(x.created_at)}</div>
                        <div className="truncate text-caption" title={x.created_by}>{x.created_by}</div>
                      </TD>
                      <TD pinEnd className="w-0" onClick={(e) => e.stopPropagation()}>
                        <Button size="xs" variant="ghost" className="text-foreground" aria-label={`Open request ${x.ref}`} onClick={() => setCaseId(x.id)}>Open</Button>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            )}
          </SectionCard>
        </>
      )}
    </PageShell>
  );
}

function NewRequest({ onCancel, onOpened }: { onCancel: () => void; onOpened: (id: string) => void }) {
  const now = useMemo(() => new Date(), []);
  const [purpose, setPurpose] = useState<"ACCESS" | "ERASURE">("ACCESS");
  const [q, setQ] = useState("");
  const [matches, setMatches] = useState<Match[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [anchors, setAnchors] = useState<Anchor[]>([]);
  const [addresses, setAddresses] = useState("");
  const [from, setFrom] = useState(localInput(new Date(now.getTime() - 7 * 86400000)));
  const [to, setTo] = useState(localInput(now));
  const [requester, setRequester] = useState("");
  const [method, setMethod] = useState("ID_CHECKED_IN_PERSON");
  const [evidence, setEvidence] = useState("");
  const [identity, setIdentity] = useState(false);
  const [device, setDevice] = useState(false);
  const [sole, setSole] = useState(false);
  const [priv, setPriv] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const parsed = useMemo(() => parseAddresses(addresses), [addresses]);
  const anyPrivate = parsed.ok.some(isPrivateAddress);
  const anyGroup = parsed.ok.some(isGroupAddress);

  async function search(e?: React.FormEvent) {
    e?.preventDefault();
    const term = q.trim();
    if (term.length < 2) return;
    setSearching(true); setErr(null);
    try {
      const r = await api.get<{ matches: Match[] }>("/guest-data-rights/search", { "X-Guest-Data-Search": encodeURIComponent(term) });
      setMatches(r.matches ?? []);
    } catch (e2) { setErr(errMsg(e2)); } finally { setSearching(false); }
  }

  const problems: string[] = [];
  if (anchors.length === 0) problems.push("Name the reservation, guest or account the records are about.");
  if (parsed.ok.length === 0) problems.push("Give at least one device address.");
  if (parsed.bad.length > 0) problems.push(`Not a device address: ${parsed.bad.join(", ")}.`);
  if (parsed.ok.length > 5) problems.push("At most five addresses in one request.");
  if (anyGroup) problems.push("A group address names no single device.");
  const fromD = new Date(from), toD = new Date(to);
  if (!(fromD < toD)) problems.push("The range must end after it starts.");
  else if (toD.getTime() - fromD.getTime() > 366 * 86400000) problems.push("The range covers at most 366 days.");
  else if (toD.getTime() > Date.now() + 60000) problems.push("The range cannot end in the future.");
  if (requester.trim().length < 2) problems.push("Say who is asking.");
  if (evidence.trim().length < 9) problems.push("Describe the evidence you saw.");
  if (!identity) problems.push("Verify the requester before you open a request.");
  if (reason.trim().length < 4) problems.push("Give a reason that names nobody.");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (problems.length > 0) return;
    setBusy(true); setErr(null);
    try {
      const r = await api.post<{ id: string }>("/guest-data-rights/assisted", {
        purpose, addresses: parsed.ok, anchors: anchors.map((a) => ({ kind: a.kind, id: a.id })),
        range_from: fromD.toISOString(), range_to: toD.toISOString(),
        requester: requester.trim(), verification_method: method, evidence_note: evidence.trim(),
        statements: { identity_verified: identity, device_shown: device, sole_use: sole, private_address_confirmed: priv },
        reason: reason.trim(),
      });
      onOpened(r.id);
    } catch (e2) { setErr(errMsg(e2)); } finally { setBusy(false); }
  }

  return (
    <SectionCard
      title="New assisted request"
      description="Nothing is handed over or changed by opening a request. You review the records first."
    >
        <form className="space-y-6" onSubmit={submit}>
          <fieldset className="space-y-2">
            <legend className="text-label">What the requester asks for</legend>
            {(["ACCESS", "ERASURE"] as const).map((p) => (
              <label key={p} className="flex items-start gap-2 text-sm">
                <input type="radio" name="purpose" className="mt-1" checked={purpose === p} onChange={() => setPurpose(p)} />
                <span>{PURPOSE_WORDS[p]}{p === "ACCESS" ? " (Art. 15)" : " (Art. 17) — only addresses are removed; totals and financial records stay"}</span>
              </label>
            ))}
          </fieldset>

          <fieldset className="space-y-3">
            <legend className="text-label">Whose records these are said to be</legend>
            <p className="text-xs text-muted-foreground">
              What the requester names — not proof. A returning guest names every reservation the device was used under.
            </p>
            <div className="flex flex-wrap gap-2">
              {anchors.map((a) => (
                <span key={a.id} className="inline-flex items-center gap-1 rounded-full border border-border px-2 py-0.5 text-sm">
                  {ANCHOR_LABELS[a.kind]}: {a.label ?? a.id.slice(0, 8)}
                  <button type="button" aria-label={`Remove ${a.label ?? a.id}`} className="text-muted-foreground hover:text-foreground"
                    onClick={() => setAnchors((x) => x.filter((y) => y.id !== a.id))}><X className="size-3" /></button>
                </span>
              ))}
            </div>
            <div className="flex flex-wrap gap-2">
              <Input aria-label="Search for the guest" className="min-w-0 flex-1" value={q} maxLength={200} onChange={(e2) => setQ(e2.target.value)}
                placeholder="name, room, reservation number, account or email" />
              <Button type="button" variant="secondary" disabled={searching || q.trim().length < 2} onClick={() => void search()}>
                <Search /> {searching ? "Searching…" : "Search"}
              </Button>
            </div>
            {matches !== null && matches.length === 0 && <p className="text-sm text-muted-foreground">No record found.</p>}
            {matches !== null && matches.length > 0 && (
              <ul className="divide-y divide-border rounded-md border border-border">
                {matches.map((m) => (
                  <li key={`${m.kind}-${m.id}`} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-sm">
                    <span>{ANCHOR_LABELS[m.kind]} · <span className="break-all">{m.label}</span>{m.detail ? <span className="text-muted-foreground"> · {m.detail}</span> : null}</span>
                    <Button type="button" size="sm" variant="secondary" disabled={anchors.length >= 5 || anchors.some((a) => a.id === m.id)}
                      aria-label={`Use ${ANCHOR_LABELS[m.kind]} ${m.label}`}
                      onClick={() => setAnchors((x) => [...x, { kind: m.kind, id: m.id, label: m.label }])}>Use</Button>
                  </li>
                ))}
              </ul>
            )}
          </fieldset>

          <fieldset className="grid gap-4 sm:grid-cols-2">
            <legend className="sr-only">Devices and range</legend>
            <Field label="Device addresses" hint="One to five MAC addresses, one per line. The requester shows each on their device.">
              <Textarea rows={3} value={addresses} onChange={(e2) => setAddresses(e2.target.value)} placeholder="3c:22:fb:0a:11:7e" />
            </Field>
            <div className="grid gap-4">
              <Field label="From"><Input type="datetime-local" value={from} onChange={(e2) => setFrom(e2.target.value)} /></Field>
              <Field label="To" hint="Records must lie entirely inside the range. At most 366 days.">
                <Input type="datetime-local" value={to} onChange={(e2) => setTo(e2.target.value)} />
              </Field>
            </div>
          </fieldset>

          <fieldset className="space-y-3">
            <legend className="text-label">How you verified the requester</legend>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Who is asking" hint="A name or a reference, kept only while the request is open."><Input value={requester} maxLength={120} onChange={(e2) => setRequester(e2.target.value)} /></Field>
              <Field label="How they were verified">
                <Select value={method} onChange={(e2) => setMethod(e2.target.value)}>
                  {METHOD_OPTIONS.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
                </Select>
              </Field>
            </div>
            <Field label="Evidence you saw" hint="Describe it — do not copy document numbers. Removed when the request closes.">
              <Textarea rows={2} value={evidence} maxLength={1000} onChange={(e2) => setEvidence(e2.target.value)} />
            </Field>
            <div className="space-y-2 text-sm">
              <label className="flex items-start gap-2"><input type="checkbox" className="mt-1" checked={identity} onChange={(e2) => setIdentity(e2.target.checked)} />
                <span>I have verified who the requester is.</span></label>
              <label className="flex items-start gap-2"><input type="checkbox" className="mt-1" checked={device} onChange={(e2) => setDevice(e2.target.checked)} />
                <span>The requester showed me the device and its address.</span></label>
              <label className="flex items-start gap-2"><input type="checkbox" className="mt-1" checked={sole} onChange={(e2) => setSole(e2.target.checked)} />
                <span>The requester states that nobody else used this device in this range.</span></label>
              {anyPrivate && (
                <label className="flex items-start gap-2"><input type="checkbox" className="mt-1" checked={priv} onChange={(e2) => setPriv(e2.target.checked)} />
                  <span>The device’s own settings show this private (randomised) address for this network.</span></label>
              )}
              {!(device && sole) && <p className="text-xs text-muted-foreground">Without the second and third statements, shared access stays unselectable: only records made with the requester’s own account can be selected.</p>}
            </div>
          </fieldset>

          <Field label="Reason" hint="Kept in the activity log after everything else is removed. Do not name the guest, and leave out addresses, emails and phone numbers.">
            <Input value={reason} maxLength={500} onChange={(e2) => setReason(e2.target.value)} placeholder="e.g. written request received at reception" />
          </Field>

          <ErrorBanner err={err} />
          {problems.length > 0 && <ul className="list-disc ps-5 text-xs text-muted-foreground">{problems.map((p) => <li key={p}>{p}</li>)}</ul>}
          <div className="flex gap-2">
            <Button type="submit" variant="primary" disabled={busy || problems.length > 0}>{busy ? "Opening…" : "Open request"}</Button>
            <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
          </div>
        </form>
    </SectionCard>
  );
}

function CasePanel({ id, onClose, onChanged }: { id: string; onClose: () => void; onChanged: () => Promise<void> }) {
  const toast = useToast();
  const [c, setC] = useState<Detail | null>(null);
  const [review, setReview] = useState<Review | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [acting, setActing] = useState<"export" | "erase" | "complete" | "cancel" | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [receipt, setReceipt] = useState<{ title: string; body: unknown } | null>(null);

  const key = (r: { kind: string; id: string }) => `${r.kind}:${r.id}`;

  const load = useCallback(async (keepNotice = false) => {
    setErr(null); if (!keepNotice) setNotice(null);
    try {
      const d = await api.get<Detail>(`/guest-data-rights/assisted/${id}`);
      setC(d);
      if (d.status === "OPEN") {
        const rv = await api.get<Review>(`/guest-data-rights/assisted/${id}/review`);
        setReview(rv); setPicked(new Set());
      } else { setReview(null); }
    } catch (e) { setErr(errMsg(e)); }
  }, [id]);
  useEffect(() => { void load(); }, [load]);

  const records = review?.records ?? [];
  const selectable = records.filter((r) => r.selectable);
  const chosen = records.filter((r) => picked.has(key(r)));
  const open = c?.status === "OPEN";
  const exported = (c?.events ?? []).filter((e) => e.action === "EXPORTED").length;

  function refusal(e: unknown): string {
    if (e instanceof ApiError && e.code === "assisted_refused") {
      const code = typeof e.body?.code === "string" ? e.body.code : "";
      if (code === "changed" || code === "not_listed" || code === "not_eligible") void load(true);
      return REFUSAL_WORDS[code] ?? errMsg(e);
    }
    return errMsg(e);
  }

  async function confirmAct(verb: "export" | "erase", { reason, password }: { reason: string; password: string }) {
    if (!c || !review) return;
    setBusy(true); setActionErr(null);
    try {
      const body = {
        password, reason, confirm: c.ref, signature: review.signature,
        selected: chosen.map((r) => ({ kind: r.kind, id: r.id })),
      };
      const r = await api.post<any>(`/guest-data-rights/assisted/${c.id}/${verb}`, body);
      setActing(null);
      if (verb === "export") {
        saveJSON(r, "assisted-guest-data");
        toast.success("Records downloaded", "Hand them to the requester only after checking who they are, then close the request.");
        await load();
      } else {
        setReceipt({ title: `Anonymised. Erasure reference ${r.erasure_ref}`, body: r });
        setNotice(`Anonymised. Reference ${r.erasure_ref}: give it to the requester as proof.`);
        await load(true); await onChanged();
      }
    } catch (e) { setActionErr(refusal(e)); } finally { setBusy(false); }
  }

  async function confirmClose(outcome: "COMPLETED" | "CANCELLED", { reason }: { reason: string; password: string }) {
    if (!c) return;
    setBusy(true); setActionErr(null);
    try {
      const r = await api.post<any>(`/guest-data-rights/assisted/${c.id}/close`, { outcome, reason });
      setActing(null);
      setReceipt({ title: outcome === "COMPLETED" ? "Request completed" : "Request cancelled", body: r.receipt });
      setNotice(outcome === "COMPLETED" ? "Request completed." : "Request cancelled.");
      await load(true); await onChanged();
    } catch (e) { setActionErr(refusal(e)); } finally { setBusy(false); }
  }

  if (err && !c) return <><ErrorBanner err={err} /><Button variant="secondary" onClick={onClose}>Back</Button></>;
  if (!c) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const tick = (v: boolean) => <StatusBadge tone={v ? "ok" : "default"}>{v ? "Yes" : "No"}</StatusBadge>;

  return (
    <div className="space-y-4">
      <div><Button variant="ghost" size="sm" className="-ms-2.5" onClick={onClose}><ArrowLeft className="rtl:rotate-180" /> All requests</Button></div>
      {notice && <Callout tone="success">{notice}</Callout>}
      {receipt && (
        <Callout tone="info" title={receipt.title}>
          This is the hotel’s file of the request. The database no longer keeps the device addresses or notes: save it with your records.{" "}
          <Button size="sm" variant="secondary" onClick={() => saveJSON(receipt.body, `assisted-request-${c.ref}`)}><Download /> Save the request file</Button>
        </Callout>
      )}
      <ErrorBanner err={err} />

      <SectionCard
        title={<span className="flex flex-wrap items-center gap-2">Request <span className="font-mono">{c.ref}</span> <StatusBadge tone={toneOf(c.status)}>{statusWord(c.status)}</StatusBadge></span>}
        description={`${PURPOSE_WORDS[c.purpose]}. Opened ${formatDate(c.created_at)} by ${c.created_by}.`}
        bodyClassName="space-y-3 text-sm"
      >
        <DescriptionList
          columns={2}
          items={[
            { label: "Range", value: `${formatDate(c.range_from)} – ${formatDate(c.range_to)}` },
            { label: "Verified", value: `${METHOD_LABEL[c.verification_method] ?? c.verification_method}${c.requester && c.requester !== "[removed]" ? ` · ${c.requester}` : ""}` },
            { label: "Device addresses", value: <span className="font-mono">{(c.addresses ?? []).join(", ")}</span>, hidden: !(open && c.addresses) },
            { label: "About", value: c.anchors.map((a) => `${ANCHOR_LABELS[a.kind]} ${a.label ?? ""}`.trim()).join("; "), hidden: !(open && c.anchors.length > 0) },
            { label: "Identity verified", value: tick(c.statements.identity_verified) },
            { label: "Device shown", value: tick(c.statements.device_shown) },
            { label: "Sole use stated", value: tick(c.statements.sole_use) },
            { label: "Private address confirmed", value: tick(c.statements.private_address_confirmed) },
            {
              label: "Result",
              hidden: !(!open && c.result),
              value: `${Object.entries(c.result ?? {}).filter(([, v]) => typeof v === "number" && v > 0).map(([k, v]) => `${v} ${k.replace(/_/g, " ")}`).join(", ") || "nothing changed"}.`,
            },
          ]}
        />
        {!open && <p className="text-muted-foreground">Closed {c.closed_at ? formatDate(c.closed_at) : ""} by {c.closed_by}. The addresses and notes were removed when it closed.</p>}
      </SectionCard>

      {open && review && (
        <SectionCard
          title="Records in this range"
          description={
            <>
              {records.length} record{records.length === 1 ? "" : "s"} for the stated addresses.{" "}
              {(Object.keys(CLASS_WORDS) as RecordClass[]).filter((k) => review.by_class[k]).map((k) => `${review.by_class[k]} ${CLASS_WORDS[k].label.toLowerCase()}`).join(" · ")}
            </>
          }
          bodyClassName="space-y-3"
        >
            {records.length === 0 && <EmptyState size="sm" title="No record of these addresses in this range" hint="Check the addresses and the range, or cancel the request." />}
            {selectable.length > 0 && (
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <Button size="sm" variant="secondary" onClick={() => setPicked(new Set(selectable.map(key)))}>Select all that can be selected</Button>
                <Button size="sm" variant="ghost" onClick={() => setPicked(new Set())}>Clear</Button>
                <span className="text-muted-foreground">{chosen.length} selected</span>
              </div>
            )}
            {records.length > 0 && (
              <div className="overflow-hidden rounded-md border border-border">
                <Table label="Records in this range" density="compact">
                  <THead><TR><TH><span className="sr-only">Select</span></TH><TH>Record</TH><TH>When</TH><TH>What it would disclose</TH><TH>Why</TH></TR></THead>
                  <TBody>
                    {records.map((r) => (
                      <TR key={key(r)} className={r.selectable ? "" : "text-muted-foreground"}>
                        <TD>
                          <input type="checkbox" aria-label={`Select ${KIND_WORDS[r.kind] ?? r.kind} ${formatDate(r.at)}`}
                            disabled={!r.selectable} checked={picked.has(key(r))}
                            onChange={(e) => setPicked((p) => { const n = new Set(p); if (e.target.checked) n.add(key(r)); else n.delete(key(r)); return n; })} />
                        </TD>
                        <TD>{KIND_WORDS[r.kind] ?? r.kind}</TD>
                        <TD className="whitespace-nowrap">{formatDate(r.at)}</TD>
                        <TD className="break-all font-mono text-xs">{r.detail ? describeDetail(r.kind, r.detail) : "—"}</TD>
                        <TD>
                          <Badge tone={CLASS_WORDS[r.class]?.tone ?? "neutral"}>{CLASS_WORDS[r.class]?.label ?? r.class}</Badge>
                          <div className="mt-1 text-xs">{REASON_WORDS[r.reason] ?? r.reason}</div>
                          {r.erase_block && c.purpose === "ERASURE" && <div className="mt-1 text-xs text-warning-subtle-foreground">{BLOCK_WORDS[r.erase_block] ?? r.erase_block}</div>}
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </div>
            )}
            <ul className="space-y-1 text-xs text-muted-foreground">
              {(Object.keys(CLASS_WORDS) as RecordClass[]).filter((k) => review.by_class[k]).map((k) => <li key={k}><strong>{CLASS_WORDS[k].label}.</strong> {CLASS_WORDS[k].help}</li>)}
            </ul>
        </SectionCard>
      )}

      {open && (
        <div className="flex flex-wrap gap-2">
          {c.purpose === "ACCESS" ? (
            <Button variant="primary" disabled={chosen.length === 0} onClick={() => { setActionErr(null); setActing("export"); }}>
              <Download /> Download {chosen.length} selected record{chosen.length === 1 ? "" : "s"}
            </Button>
          ) : (
            <Button variant="danger" disabled={chosen.length === 0} onClick={() => { setActionErr(null); setActing("erase"); }}>
              <Eraser /> Anonymise {chosen.length} selected record{chosen.length === 1 ? "" : "s"}
            </Button>
          )}
          {c.purpose === "ACCESS" && exported > 0 && (
            <Button variant="secondary" onClick={() => { setActionErr(null); setActing("complete"); }}>Mark completed</Button>
          )}
          <Button variant="ghost" onClick={() => { setActionErr(null); setActing("cancel"); }}>Cancel request</Button>
        </div>
      )}

      {c.events.length > 0 && (
        <SectionCard flush title="History">
          <HistoryList
            label="Request history"
            items={c.events.map((e, i) => ({ id: String(i), title: statusWord(e.action.replace(/_/g, " ")), actor: e.by, at: formatDate(e.at) }))}
          />
        </SectionCard>
      )}

      <ConfirmDialog
        open={acting === "export"} onOpenChange={(v) => { if (!v) { setActing(null); setActionErr(null); } }}
        title="Hand over the selected records?" description={ASSISTED_EXPORT_NOTE}
        confirmLabel="Download records" busy={busy} error={actionErr}
        confirmText={c.ref} requireReason reasonStrict reasonMinLength={4} reasonPlaceholder="e.g. written request received at reception" requirePassword
        onConfirm={(a) => confirmAct("export", a)}
      />
      <ConfirmDialog
        open={acting === "erase"} onOpenChange={(v) => { if (!v) { setActing(null); setActionErr(null); } }}
        title="Anonymise the selected records?" confirmLabel="Anonymise records" confirmVariant="danger" busy={busy} error={actionErr}
        confirmText={c.ref} consequences={ASSISTED_ERASE_CONSEQUENCES}
        requireReason reasonStrict reasonMinLength={4} reasonPlaceholder="e.g. written erasure request received at reception" requirePassword
        onConfirm={(a) => confirmAct("erase", a)}
      />
      <ConfirmDialog
        open={acting === "complete"} onOpenChange={(v) => { if (!v) { setActing(null); setActionErr(null); } }}
        title="Mark this request completed?" description="The device addresses and notes are removed from the database. You will be offered the request file to save."
        confirmLabel="Complete request" busy={busy} error={actionErr} requireReason reasonStrict reasonMinLength={4} reasonPlaceholder="e.g. records handed over in person"
        onConfirm={(a) => confirmClose("COMPLETED", a)}
      />
      <ConfirmDialog
        open={acting === "cancel"} onOpenChange={(v) => { if (!v) { setActing(null); setActionErr(null); } }}
        title="Cancel this request?" description="Nothing is handed over or changed. The device addresses and notes are removed from the database."
        confirmLabel="Cancel request" confirmVariant="danger" busy={busy} error={actionErr} requireReason reasonStrict reasonMinLength={4} reasonPlaceholder="e.g. the requester withdrew"
        onConfirm={(a) => confirmClose("CANCELLED", a)}
      />
    </div>
  );
}
