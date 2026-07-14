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

  const writable = canWrite("network", roles);

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
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">Networking</div>
        <h1 className="text-2xl font-semibold">Config history</h1>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

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
                {rows.map((r) => (
                  <Fragment key={r.id}>
                    <TR className="cursor-pointer" onClick={() => toggle(r.id)}>
                      <TD className="font-mono">#{r.seq}</TD>
                      <TD><Badge tone={stateTone(r.state)}>{r.state}</Badge></TD>
                      <TD>{r.summary || "—"}</TD>
                      <TD className="text-muted">{r.applied_at ? formatDate(r.applied_at) : "—"}</TD>
                      <TD className="text-muted">{r.confirmed_at ? formatDate(r.confirmed_at) : "—"}</TD>
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
                    <span className="font-mono">{i.field}</span> — {i.message} <span className="text-muted">({i.code})</span>
                  </li>
                ))}
              </ul>
            )}
          </>
        ) : <span className="text-muted text-xs">—</span>}
      </div>

      <div>
        <div className="text-muted text-xs uppercase tracking-wider mb-1">Apply events</div>
        {d.events && d.events.length > 0 ? (
          <ul className="space-y-1">
            {d.events.map((e, k) => (
              <li key={k} className="flex items-center gap-2 text-xs">
                <Badge tone={e.ok ? "ok" : "err"}>{e.ok ? "ok" : "fail"}</Badge>
                <span className="font-mono">{e.phase}</span>
                {e.at && <span className="text-muted">{formatDate(e.at)}</span>}
                {e.detail != null && <span className="text-muted">{typeof e.detail === "string" ? e.detail : JSON.stringify(e.detail)}</span>}
              </li>
            ))}
          </ul>
        ) : <span className="text-muted text-xs">—</span>}
      </div>

      <div>
        <div className="text-muted text-xs uppercase tracking-wider mb-1">Health checks</div>
        {d.health && d.health.length > 0 ? (
          <ul className="space-y-1">
            {d.health.map((h, k) => (
              <li key={k} className="flex items-center gap-2 text-xs">
                <Badge tone={h.ok ? "ok" : "err"}>{h.ok ? "ok" : "fail"}</Badge>
                <span className="font-mono">{h.check_name}</span>
                {h.detail && <span className="text-muted">{h.detail}</span>}
                {h.at && <span className="text-muted">{formatDate(h.at)}</span>}
              </li>
            ))}
          </ul>
        ) : <span className="text-muted text-xs">—</span>}
      </div>
    </div>
  );
}
