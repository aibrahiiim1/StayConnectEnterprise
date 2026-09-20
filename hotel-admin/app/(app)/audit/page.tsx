"use client";

// ACTIVITY — who changed what, and when.
//
// WHAT THIS REPLACES. A raw table of the audit rows: a timestamp, an actor uuid, a dotted action code, a
// target uuid, an IP and a payload column that said "—". Every fact was present and none of it was legible.
// An operator asking "who turned the guest network off on Tuesday" had to know that the answer looked like
// `network.guest.disabled`, and then read two uuids to find out who and which.
//
// The trail itself is unchanged and untouchable: append-only, nothing rewritten, nothing backfilled. What
// changed is that the screen now reads it out loud. Each row leads with a sentence, a person and a time; the
// exact action code, the ids, the address and the payload are one click away, because the day this screen
// matters most is the day somebody needs the precise record rather than the readable one.
//
// SEVERITY IS NOT INVENTED. Three levels, assigned per action in lib/audit-words.ts: a notice (something
// happened), a change (configuration moved) and a security event (someone saw a guest's typed credentials,
// a backup left the appliance, an unlicensed-mode attempt was refused). Anything unrecognised is shown with
// its code intact rather than guessed at.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { SkeletonRows } from "@/components/ui/misc";
import { formatDate } from "@/lib/utils";
import {
  auditWords, auditActor, AUDIT_CATEGORIES, SEVERITY_TONE, type AuditCategory,
} from "@/lib/audit-words";
import { ScrollText, Search, ChevronRight, ShieldAlert } from "lucide-react";

type Row = {
  ts: string;
  actor_type?: string | null;
  actor_id?: string | null;
  action: string;
  target_type?: string | null;
  target_id?: string | null;
  ip?: string | null;
  payload?: unknown;
};

/** The prefix that selects a category server-side. One clause instead of enumerating every code. */
const CATEGORY_PREFIX: Record<AuditCategory, string[]> = {
  "Sign-in & access": ["operator", "session", "guest_signin", "auth_methods"],
  "Guest portal": ["branding", "portal_asset"],
  "Internet offering": ["commercial_package", "service_plan", "checkout_grace"],
  "Property management system": ["pms_"],
  "Networks": ["network"],
  "Licence & cloud": ["license", "cloud", "renewal", "hotel_admin_cert"],
  "Backups": ["backup"],
  "Diagnostics": ["health"],
};

const RANGES = [
  { label: "Last 24 hours", hours: 24 },
  { label: "Last 7 days", hours: 24 * 7 },
  { label: "Last 30 days", hours: 24 * 30 },
  { label: "Everything", hours: 0 },
];

export default function ActivityPage() {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [range, setRange] = useState(RANGES[2]);
  const [category, setCategory] = useState<AuditCategory | "">("");
  const [securityOnly, setSecurityOnly] = useState(false);
  const [text, setText] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  const load = useCallback(async () => {
    setBusy(true); setErr(null);
    try {
      const p = new URLSearchParams({ limit: "500" });
      if (range.hours) p.set("from", new Date(Date.now() - range.hours * 3600_000).toISOString());
      // A category becomes one prefix clause. Several prefixes per category means several requests would be
      // wrong; instead the widest prefix is sent and the rest is narrowed below, on a page the server sized.
      if (category) {
        const prefixes = CATEGORY_PREFIX[category];
        if (prefixes.length === 1) p.set("action_prefix", prefixes[0]);
      }
      const r = await api.get<{ data: Row[] }>(`/audit?${p.toString()}`);
      setRows(r.data ?? []);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "the activity trail could not be read");
      setRows(null);
    } finally { setBusy(false); }
  }, [range, category]);

  useEffect(() => { load(); }, [load]);

  const visible = useMemo(() => {
    if (!rows) return null;
    const q = text.trim().toLowerCase();
    return rows.filter((r) => {
      const w = auditWords(r.action);
      if (category && w.category !== category) return false;
      if (securityOnly && w.severity !== "security") return false;
      if (!q) return true;
      return (
        w.title.toLowerCase().includes(q) ||
        r.action.toLowerCase().includes(q) ||
        (r.actor_id ?? "").toLowerCase().includes(q) ||
        (r.target_id ?? "").toLowerCase().includes(q) ||
        (r.ip ?? "").toLowerCase().includes(q)
      );
    });
  }, [rows, text, category, securityOnly]);

  const securityCount = useMemo(
    () => (rows ?? []).filter((r) => auditWords(r.action).severity === "security").length,
    [rows],
  );

  return (
    <div className="mx-auto w-full max-w-6xl space-y-5">
      <header>
        <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">System</div>
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight sm:text-2xl">
          <ScrollText className="h-5 w-5" /> Activity
        </h1>
        <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
          Every change made to this appliance, by staff and by the system itself. The record is written as it
          happens and is never edited or removed.
        </p>
      </header>

      {err && (
        <div role="alert" className="rounded-lg border border-destructive/25 bg-destructive-subtle p-3 text-sm text-destructive-subtle-foreground">
          {err}
        </div>
      )}

      <Card>
        <CardBody className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <label className="relative min-w-0 flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
              <Input value={text} onChange={(e) => setText(e.target.value)} className="pl-8"
                aria-label="Search the activity trail"
                placeholder="Search — what happened, who, an address, an id" />
            </label>
            <select
              aria-label="Period"
              className="h-9 rounded-md border bg-card px-2 text-sm"
              value={range.label}
              onChange={(e) => setRange(RANGES.find((x) => x.label === e.target.value) ?? RANGES[2])}
            >
              {RANGES.map((r) => <option key={r.label}>{r.label}</option>)}
            </select>
            <Button variant="secondary" onClick={load} disabled={busy}>{busy ? "Reading…" : "Refresh"}</Button>
          </div>

          <div className="flex flex-wrap gap-1.5">
            <Chip on={category === "" && !securityOnly} onClick={() => { setCategory(""); setSecurityOnly(false); }}>
              Everything
            </Chip>
            {/* SECURITY EVENTS FIRST, because that is the filter somebody reaches for under pressure. */}
            <Chip on={securityOnly} onClick={() => { setSecurityOnly((v) => !v); setCategory(""); }}>
              <ShieldAlert className="mr-1 inline h-3.5 w-3.5" />
              Security {securityCount > 0 && <span className="ml-1 opacity-70">{securityCount}</span>}
            </Chip>
            {AUDIT_CATEGORIES.map((c) => (
              <Chip key={c} on={category === c} onClick={() => { setCategory(category === c ? "" : c); setSecurityOnly(false); }}>
                {c}
              </Chip>
            ))}
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>
            {visible === null ? "Activity" : `${visible.length} ${visible.length === 1 ? "entry" : "entries"}`}
          </CardTitle>
        </CardHeader>
        <CardBody>
          {visible === null ? <SkeletonRows rows={6} cols={3} /> : visible.length === 0 ? (
            <EmptyState icon={<ScrollText />} title="Nothing matches"
              hint="No recorded activity fits these filters. Widen the period or clear the search." />
          ) : (
            <ul className="divide-y">
              {visible.map((r, i) => {
                const w = auditWords(r.action);
                const id = `${r.ts}-${i}`;
                const isOpen = open === id;
                return (
                  <li key={id} className="py-3">
                    <button
                      type="button"
                      className="flex w-full items-start gap-3 text-left"
                      aria-expanded={isOpen}
                      onClick={() => setOpen(isOpen ? null : id)}
                    >
                      <ChevronRight className={`mt-1 h-4 w-4 shrink-0 text-muted-foreground transition-transform ${isOpen ? "rotate-90" : ""}`} />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-medium">{w.title}</span>
                          {w.severity === "security" && <Badge tone="warn">Security</Badge>}
                          <Badge tone="default">{w.category}</Badge>
                        </div>
                        <div className="mt-0.5 text-xs text-muted-foreground">
                          {auditActor(r.actor_type, r.actor_id)} · {formatDate(r.ts)}
                          {r.ip ? ` · from ${r.ip.replace(/\/\d+$/, "")}` : ""}
                          {r.target_type ? ` · ${r.target_type}` : ""}
                        </div>
                      </div>
                    </button>

                    {isOpen && (
                      <div className="ml-7 mt-3 space-y-2 rounded-md border bg-surface p-3 text-xs">
                        {w.note && <p className="text-muted-foreground">{w.note}</p>}
                        {/* THE EXACT RECORD. Unchanged, for the day the readable version is not enough. */}
                        <dl className="grid grid-cols-[9rem_1fr] gap-x-4 gap-y-1">
                          <Detail k="Recorded" v={new Date(r.ts).toISOString()} />
                          <Detail k="Action code" v={r.action} mono />
                          <Detail k="Actor" v={r.actor_id || `(${r.actor_type ?? "system"})`} mono />
                          <Detail k="Target" v={r.target_id ? `${r.target_type ?? ""} ${r.target_id}`.trim() : "—"} mono />
                          <Detail k="Source address" v={r.ip || "—"} mono />
                        </dl>
                        {r.payload !== null && r.payload !== undefined && (
                          <div>
                            <div className="mb-1 text-muted-foreground">Details</div>
                            <pre className="overflow-x-auto rounded bg-card p-2 font-mono text-2xs">
                              {JSON.stringify(r.payload, null, 2)}
                            </pre>
                          </div>
                        )}
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </CardBody>
      </Card>
    </div>
  );
}

function Chip({ on, onClick, children }: { on: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" onClick={onClick} aria-pressed={on}
      className={`rounded-full border px-3 py-1.5 text-xs ${on ? "border-primary bg-primary/5 text-primary" : "text-muted-foreground"}`}>
      {children}
    </button>
  );
}

function Detail({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-muted-foreground">{k}</dt>
      <dd className={`m-0 break-all ${mono ? "font-mono" : ""}`}>{v}</dd>
    </>
  );
}
