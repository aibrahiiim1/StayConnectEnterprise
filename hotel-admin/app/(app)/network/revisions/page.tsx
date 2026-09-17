"use client";

import { Fragment, useEffect, useState } from "react";
import {
  api, ListResp, Whoami,
  NetRevision, NetRevisionDetail,
} from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { canWrite } from "@/lib/roles";
import { errMsg, formatDate } from "@/lib/utils";

function stateTone(state: string): "ok" | "warn" | "err" | "default" {
  switch (state) {
    case "active":               return "ok";
    case "pending_confirmation": return "warn";
    case "rolled_back":
    case "failed":               return "err";
    default:                     return "default";
  }
}

export default function RevisionsPage() {
  const [rows, setRows] = useState<NetRevision[] | null>(null);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [detail, setDetail] = useState<Record<string, NetRevisionDetail>>({});
  /** Clutter control. Nothing here removes a revision -- see the note in the header for why it must not. */
  const [filter, setFilter] = useState<"needs-attention" | "all">("all");
  const [showAll, setShowAll] = useState(false);

  const writable = canWrite("network", roles);

  /** What the table actually renders: the chosen filter, then a preview cap. Both are presentation only. */
  const NEEDS = new Set(["pending_confirmation", "rolled_back", "failed"]);
  const filtered = (rows ?? []).filter((r) => filter === "all" || NEEDS.has(r.state));
  const PREVIEW = 15;
  const visible = showAll ? filtered : filtered.slice(0, PREVIEW);

  async function load() {
    try { setRows((await api.get<ListResp<NetRevision>>("/network/revisions")).data ?? []); }
    catch (e) { setErr(errMsg(e)); }
  }

  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);

  async function toggle(id: string) {
    if (expanded === id) { setExpanded(null); return; }
    setExpanded(id);
    if (!detail[id]) {
      try {
        const d = await api.get<NetRevisionDetail>(`/network/revisions/${id}`);
        setDetail((m) => ({ ...m, [id]: d }));
      } catch (e) { setErr(errMsg(e)); }
    }
  }

  async function onConfirm(id: string) {
    setBusy(true); setErr(null);
    try { await api.post(`/network/revisions/${id}/confirm`); load(); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }
  async function onRollback(id: string) {
    setBusy(true); setErr(null);
    try { await api.post(`/network/revisions/${id}/rollback`); load(); }
    catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  return (
    <div className="mx-auto w-full max-w-7xl space-y-5">
      <div className="mb-4">
        <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">Networking</div>
        <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">Config history</h1>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {/* WHY THERE IS NO DELETE HERE.
          A revision is what rollback rolls back TO, and what the current configuration's provenance points
          at. Removing one does not tidy the list, it makes the remaining history misleading: a gap reads as
          "nothing happened" and a rollback target that no longer exists fails at the worst possible moment.
          So the clutter is handled by filtering and folding, and every revision stays. */}
      {rows !== null && rows.length > 0 && (
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <div className="inline-flex rounded border" role="group" aria-label="Filter revisions">
            {(["needs-attention", "all"] as const).map((f) => (
              <button
                key={f}
                type="button"
                aria-pressed={filter === f}
                onClick={() => { setFilter(f); setShowAll(false); }}
                className={
                  "px-3 py-1 " +
                  (filter === f ? "bg-brand text-white" : "text-muted hover:text-text")
                }
              >
                {f === "needs-attention" ? "Needs attention" : `All (${rows.length})`}
              </button>
            ))}
          </div>
          <span className="text-muted">
            {filter === "needs-attention"
              ? "Revisions awaiting confirmation, rolled back or failed."
              : "Every recorded validate/apply. Nothing is ever deleted — rollback and provenance depend on it."}
          </span>
        </div>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No revisions yet" hint="Every validate/apply of the network configuration is recorded here." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Seq</TH><TH>State</TH><TH>Summary</TH><TH>Applied</TH><TH>Confirmed</TH><TH>Failure</TH><TH></TH></TR>
              </THead>
              <tbody>
                {visible.map((r) => (
                  <Fragment key={r.id}>
                    <TR className="cursor-pointer" onClick={() => toggle(r.id)}>
                      <TD className="font-mono">#{r.seq}</TD>
                      <TD><Badge tone={stateTone(r.state)}>{r.state}</Badge></TD>
                      <TD>{r.summary || "—"}</TD>
                      <TD className="text-muted-foreground">{r.applied_at ? formatDate(r.applied_at) : "—"}</TD>
                      <TD className="text-muted-foreground">{r.confirmed_at ? formatDate(r.confirmed_at) : "—"}</TD>
                      <TD className="text-err text-xs max-w-xs truncate" title={r.failure_reason ?? ""}>{r.failure_reason || "—"}</TD>
                      <TD className="text-right space-x-2 whitespace-nowrap" onClick={(e) => e.stopPropagation()}>
                        {writable && r.state === "pending_confirmation" && (
                          <>
                            <Button size="sm" disabled={busy} onClick={() => onConfirm(r.id)}>Confirm</Button>
                            <Button size="sm" variant="secondary" disabled={busy} onClick={() => onRollback(r.id)}>Rollback</Button>
                          </>
                        )}
                        <Button size="sm" variant="ghost" onClick={() => toggle(r.id)}>{expanded === r.id ? "Hide" : "Details"}</Button>
                      </TD>
                    </TR>
                    {expanded === r.id && (
                      <TR>
                        <TD colSpan={7} className="bg-panel2">
                          <RevisionDetailView d={detail[r.id]} />
                        </TD>
                      </TR>
                    )}
                  </Fragment>
                ))}
              </tbody>
            </Table>
          )}
          {filtered.length > visible.length && (
            <div className="border-t p-3">
              <Button size="sm" variant="secondary" onClick={() => setShowAll(true)}>
                Show {filtered.length - visible.length} older revision{filtered.length - visible.length === 1 ? "" : "s"}
              </Button>
            </div>
          )}
          {rows !== null && rows.length > 0 && filtered.length === 0 && (
            <EmptyState
              title="Nothing needs attention"
              hint="No revision is awaiting confirmation, rolled back or failed. Switch to All to see the full history."
            />
          )}
        </CardBody>
      </Card>
    </div>
  );
}

function RevisionDetailView({ d }: { d?: NetRevisionDetail }) {
  if (!d) return <div className="text-sm text-muted py-2">Loading…</div>;
  return (
    <div className="space-y-4 py-2 text-sm">
      <div>
        <div className="text-muted text-xs uppercase tracking-wider mb-1">Validation</div>
        {d.validation ? (
          <>
            <Badge tone={d.validation.ok ? "ok" : "err"}>{d.validation.ok ? "ok" : "issues"}</Badge>
            {d.validation.issues && d.validation.issues.length > 0 && (
              <ul className="mt-1 space-y-1">
                {d.validation.issues.map((i, k) => (
                  <li key={k} className="text-err text-xs">
                    <span className="font-mono">{i.field}</span> — {i.message} <span className="text-muted-foreground">({i.code})</span>
                  </li>
                ))}
              </ul>
            )}
          </>
        ) : <span className="text-xs text-muted-foreground">—</span>}
      </div>

      <div>
        <div className="text-muted text-xs uppercase tracking-wider mb-1">Apply events</div>
        {d.events && d.events.length > 0 ? (
          <ul className="space-y-1">
            {d.events.map((e, k) => (
              <li key={k} className="flex items-center gap-2 text-xs">
                <Badge tone={e.ok ? "ok" : "err"}>{e.ok ? "ok" : "fail"}</Badge>
                <span className="font-mono">{e.phase}</span>
                {e.at && <span className="text-muted-foreground">{formatDate(e.at)}</span>}
                {e.detail != null && <span className="text-muted-foreground">{typeof e.detail === "string" ? e.detail : JSON.stringify(e.detail)}</span>}
              </li>
            ))}
          </ul>
        ) : <span className="text-xs text-muted-foreground">—</span>}
      </div>

      <div>
        <div className="text-muted text-xs uppercase tracking-wider mb-1">Health checks</div>
        {d.health && d.health.length > 0 ? (
          <ul className="space-y-1">
            {d.health.map((h, k) => (
              <li key={k} className="flex items-center gap-2 text-xs">
                <Badge tone={h.ok ? "ok" : "err"}>{h.ok ? "ok" : "fail"}</Badge>
                <span className="font-mono">{h.check_name}</span>
                {h.detail && <span className="text-muted-foreground">{h.detail}</span>}
                {h.at && <span className="text-muted-foreground">{formatDate(h.at)}</span>}
              </li>
            ))}
          </ul>
        ) : <span className="text-xs text-muted-foreground">—</span>}
      </div>
    </div>
  );
}
