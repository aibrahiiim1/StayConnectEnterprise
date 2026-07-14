"use client";

import { useEffect, useState } from "react";
import { api, ListResp, Session } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { formatBytes, formatRelative } from "@/lib/utils";

const toneFor = (state: string) =>
  state === "active" ? "info" : state === "closed" ? "default" : "warn";

export default function SessionsPage() {
  const [tab, setTab] = useState<"active" | "recent">("active");
  const [rows, setRows] = useState<Session[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  async function load() {
    setRows(null);
    const q = new URLSearchParams();
    if (tab === "active") q.set("state", "active");
    try {
      const r = await api.get<ListResp<Session>>(`/sessions?${q.toString()}`);
      setRows(r.data);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, [tab]);
  useEffect(() => {
    if (tab !== "active") return;
    const id = setInterval(load, 10000); // soft poll
    return () => clearInterval(id);
  }, [tab]);

  async function onDisconnect(sid: string) {
    if (!confirm("Disconnect this session?")) return;
    setBusy(sid); setErr(null);
    try {
      await api.post(`/sessions/${sid}/disconnect`, { reason: "admin" });
      setTimeout(load, 500);
    } catch (e: any) {
      setErr(e?.message ?? "Disconnect failed");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Access</div>
          <h1 className="text-2xl font-semibold">Sessions</h1>
        </div>
        <div className="flex gap-1 text-sm bg-panel2 border border-border rounded-md p-1">
          <button
            onClick={() => setTab("active")}
            className={`px-3 py-1 rounded ${tab === "active" ? "bg-panel text-text" : "text-muted"}`}
          >Active</button>
          <button
            onClick={() => setTab("recent")}
            className={`px-3 py-1 rounded ${tab === "recent" ? "bg-panel text-text" : "text-muted"}`}
          >Recent</button>
        </div>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <EmptyState title="Loading…" />
          ) : rows.length === 0 ? (
            <EmptyState
              title={tab === "active" ? "No active sessions" : "No recent sessions"}
              hint={tab === "active" ? "Connected guests will appear here." : undefined}
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>IP / MAC</TH>
                  <TH>State</TH>
                  <TH>Started</TH>
                  <TH>Last activity</TH>
                  <TH className="text-right">Down / Up</TH>
                  <TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((s) => (
                  <TR key={s.id}>
                    <TD>
                      <div className="font-mono">{s.ip}</div>
                      <div className="text-xs text-muted font-mono">{s.mac}</div>
                    </TD>
                    <TD>
                      <Badge tone={toneFor(s.state) as any}>{s.state}</Badge>
                      {s.end_reason && <span className="text-xs text-muted ml-2">{s.end_reason}</span>}
                    </TD>
                    <TD className="text-muted">{formatRelative(s.started_at)}</TD>
                    <TD className="text-muted">{formatRelative(s.last_activity_at)}</TD>
                    <TD className="text-right">
                      <div>{formatBytes(s.bytes_down)}</div>
                      <div className="text-xs text-muted">{formatBytes(s.bytes_up)}</div>
                    </TD>
                    <TD className="text-right">
                      {s.state === "active" && (
                        <Button
                          size="sm" variant="danger"
                          disabled={busy === s.id}
                          onClick={() => onDisconnect(s.id)}
                        >
                          {busy === s.id ? "…" : "Disconnect"}
                        </Button>
                      )}
                    </TD>
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
