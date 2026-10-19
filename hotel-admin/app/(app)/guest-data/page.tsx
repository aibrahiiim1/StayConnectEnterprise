"use client";

// GUEST DATA REQUESTS (migration 0128; GDPR plan C3 and C4; Product-Owner decisions D-C3 and D-C4).
//
// A guest asks for a copy of what the hotel holds about them, or to be erased. Staff find the guest, check who
// they are, and either download the file to hand over or erase them. Only site_admin and the IT manager reach
// this page; every export and erasure asks for the password and a reason, and is recorded in Activity by record
// id or erasure reference -- never by the guest's name or address. Erasure anonymises (no record is deleted, so
// reports and totals keep working) and is refused while the guest is still staying or online.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";
import { UserSearch, Download, Eraser, Search } from "lucide-react";

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

// Why an erasure was refused, in the words a front office understands.
const LIVE_REASONS: Record<string, string> = {
  co_guests: "other guests are named on this stay; search again and choose the guest who asked, so the others are left alone",
  pms_open: "the PMS still shows this stay as reserved or in-house and its departure date has not passed; the PMS would copy the guest straight back",
  pms_pending: "a PMS message about this stay has not been processed yet; try again once it has (see PMS activity)",
  access_live: "the guest's internet access has not ended yet",
  online: "one of the guest's devices is online right now",
  signin_restricted: "one of the guest's devices is under a sign-in restriction; release it on Guest sign-in attempts first",
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
  const [searching, setSearching] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [erasures, setErasures] = useState<Erasure[] | null>(null);

  const [exporting, setExporting] = useState<Match | null>(null);
  const [erasing, setErasing] = useState<Match | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [done, setDone] = useState<{ ref: string; counts: Record<string, number> } | null>(null);

  const loadErasures = useCallback(async () => {
    try {
      const r = await api.get<{ erasures: Erasure[] }>("/guest-data-rights/erasures");
      setErasures(r.erasures ?? []);
    } catch { setErasures([]); }
  }, []);
  useEffect(() => { void loadErasures(); }, [loadErasures]);

  async function search(e?: React.FormEvent) {
    e?.preventDefault();
    const term = q.trim();
    if (term.length < 2) return;
    setSearching(true); setErr(null);
    try {
      const r = await api.get<{ matches: Match[] }>("/guest-data-rights/search", { "X-Guest-Data-Search": encodeURIComponent(term) });
      setMatches(r.matches ?? []);
    } catch (e) { setErr(errMsg(e)); } finally { setSearching(false); }
  }

  async function doExport({ reason, password }: { reason: string; password: string }) {
    if (!exporting) return;
    setBusy(true); setActionErr(null);
    try {
      const doc = await api.post<unknown>("/guest-data-rights/export", { kind: exporting.kind, id: exporting.id, reason, password });
      saveJSON(doc);
      setExporting(null);
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
      setMatches((m) => m?.filter((x) => x.id !== erasing.id) ?? null);
      await loadErasures();
    } catch (e) {
      if (e instanceof ApiError && e.code === "guest_data_live") {
        const live: string[] = Array.isArray(e.body?.live) ? e.body.live : [];
        setActionErr("Nothing was erased: " + live.map((x) => LIVE_REASONS[x] ?? x).join("; ") + ".");
      } else setActionErr(errMsg(e));
    } finally { setBusy(false); }
  }

  return (
    <PageShell>
      <PageHeader
        icon={<UserSearch />}
        eyebrow="System"
        title="Guest data requests"
        description="Give a guest a copy of their data, or erase it, when they ask."
      />

      <Callout tone="info">
        Check the guest&rsquo;s identity before you hand over an export or erase anything. Do not write the
        guest&rsquo;s name or address in the reason: the reason is kept after the guest is erased.
      </Callout>

      {done && (
        <Callout tone="success">
          Erased. Reference <strong className="font-mono">{done.ref}</strong>: give it to the guest as proof. {""}
          {Object.entries(done.counts).filter(([, n]) => n > 0).map(([k, n]) => `${n} ${k.replace(/_/g, " ")}`).join(", ")}.
        </Callout>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Find the guest</CardTitle>
          <CardDescription>Email, phone, name, client account, room, reservation number or device MAC address.</CardDescription>
        </CardHeader>
        <CardBody className="space-y-4">
          <form className="flex flex-wrap gap-2" onSubmit={search}>
            <Input aria-label="Search" className="min-w-0 flex-1" value={q} maxLength={200}
              onChange={(e) => setQ(e.target.value)} placeholder="guest@example.com, 412, Smith, aa:bb:cc:dd:ee:ff" />
            <Button type="submit" variant="primary" disabled={searching || q.trim().length < 2}>
              <Search /> {searching ? "Searching…" : "Search"}
            </Button>
          </form>
          <ErrorBanner err={err} />
          {matches !== null && matches.length === 0 && (
            <EmptyState title="No guest found" hint="Records already erased or anonymised by retention are not shown." />
          )}
          {matches !== null && matches.length > 0 && (
            <div className="overflow-x-auto">
              <Table>
                <THead>
                  <TR><TH>Record</TH><TH>Found by</TH><TH>Status</TH><TH>Last activity</TH><TH /></TR>
                </THead>
                <TBody>
                  {matches.map((m) => (
                    <TR key={`${m.kind}-${m.id}`}>
                      <TD>
                        <div className="text-sm">{KIND_LABELS[m.kind]}</div>
                        {m.detail && <div className="text-caption text-muted-foreground">{m.detail}</div>}
                      </TD>
                      <TD className="break-all">{m.label}</TD>
                      <TD>{m.status ? <Badge tone="neutral">{m.status}</Badge> : null}</TD>
                      <TD>{m.last_activity ? formatDate(m.last_activity) : "—"}</TD>
                      <TD className="whitespace-nowrap text-right">
                        <Button size="sm" variant="outline" onClick={() => { setActionErr(null); setExporting(m); }}>
                          <Download /> Export
                        </Button>{" "}
                        <Button size="sm" variant="danger" onClick={() => { setActionErr(null); setDone(null); setErasing(m); }}>
                          <Eraser /> Erase
                        </Button>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Erasures</CardTitle>
          <CardDescription>Every erasure on this site. They hold no guest data.</CardDescription>
        </CardHeader>
        <CardBody>
          {erasures !== null && erasures.length === 0 && <EmptyState title="No guest has been erased" />}
          {erasures !== null && erasures.length > 0 && (
            <div className="overflow-x-auto">
              <Table>
                <THead><TR><TH>Reference</TH><TH>When</TH><TH>By</TH><TH>Reason</TH></TR></THead>
                <TBody>
                  {erasures.map((x) => (
                    <TR key={x.erasure_ref}>
                      <TD className="font-mono">{x.erasure_ref}</TD>
                      <TD>{formatDate(x.erased_at)}</TD>
                      <TD>{x.erased_by}</TD>
                      <TD>{x.reason}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </CardBody>
      </Card>

      <ConfirmDialog
        open={exporting !== null}
        onOpenChange={(v) => { if (!v) { setExporting(null); setActionErr(null); } }}
        title="Export this guest’s data?"
        description="Downloads everything held about this guest as one file: accounts, sign-in methods, stays, internet access, devices, sessions and sign-in attempts. When other guests share the stay, the file holds only this guest’s details, without the room’s shared internet access."
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
        consequences={[
          "Names, email addresses, phone numbers, room and reservation numbers, MAC and IP addresses are replaced or cleared. This cannot be undone.",
          "Linked records go too: the stay a client account belongs to, and the portal identities that signed in on it.",
          "Other guests on the same stay are left alone: only this guest’s name and PMS guest number are removed from it.",
          "Totals, reports and financial records stay, without anything that identifies the guest.",
          "Refused while the guest is in-house, has internet access running, or has a device online.",
        ]}
        requireReason
        reasonMinLength={4}
        reasonPlaceholder="e.g. written erasure request received at reception"
        requirePassword
        onConfirm={doErase}
      />
    </PageShell>
  );
}
