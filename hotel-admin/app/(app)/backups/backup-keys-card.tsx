"use client";

// Backups are encrypted to every holder's public key. The private key is handed over once, and encryption
// relies on it only after the printed check code is typed back.

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input, Textarea } from "@/components/ui/input";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";
import { KeyRound, Download, Printer, ShieldCheck, Lock, LockOpen } from "lucide-react";

type Holder = {
  id: string; label: string; public_key: string; status: "PENDING" | "ACTIVE" | "REMOVED";
  created_at: string; created_by: string; confirmed_at: string | null; removed_at: string | null;
  last_tested_at: string | null;
};
type Resp = {
  available: boolean; encrypted: boolean; active_holders?: number; holders: Holder[];
  restore_test?: { interval_months: number; last_tested_at: string | null; due_at: string | null; overdue: boolean };
};
type NewKey = {
  id: string; label: string; public_key: string; created_at: string; private_key: string; check_code: string;
  key_file: string; key_file_name: string;
};

/** Held in component state for one action only. */
export function BackupKeyInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <Field label="Backup key" hint="Paste the key file’s contents, or choose the key file. It is used for this action only and never stored.">
      <div className="space-y-2">
        <Textarea aria-label="Backup key" rows={3} className="font-mono text-xs" spellCheck={false} autoComplete="off"
          value={value} onChange={(e) => onChange(e.target.value)} placeholder="AGE-SECRET-KEY-1…" />
        <input type="file" accept=".txt,text/plain" aria-label="Choose key file" className="text-sm"
          onChange={async (e) => { const f = e.target.files?.[0]; if (f) onChange(await f.text()); e.target.value = ""; }} />
      </div>
    </Field>
  );
}

function downloadText(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  URL.revokeObjectURL(url);
}

function esc(s: string) {
  return s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]!));
}

function printSheet(k: NewKey) {
  const w = window.open("", "_blank", "width=800,height=900");
  if (!w) return;
  w.document.write(`<!doctype html><html><head><meta charset="utf-8"><title>Backup key: ${esc(k.label)}</title>
<style>body{font:14px/1.5 system-ui,sans-serif;margin:32px;color:#111}h1{font-size:20px}code{font:15px/1.6 ui-monospace,monospace;word-break:break-all}
.box{border:2px solid #111;padding:16px;margin:16px 0}.check{font:24px ui-monospace,monospace;letter-spacing:2px}</style></head><body>
<h1>StayConnect backup key — ${esc(k.label)}</h1>
<p>Created ${esc(new Date(k.created_at).toLocaleString())}. Keep this sheet somewhere only authorised people can reach, such as the hotel safe.
Anyone with this key can read this hotel’s backups. Without it, or another key holder’s key, nobody can — not the appliance and not its supplier.</p>
<div class="box"><div>Private key</div><code>${esc(k.private_key)}</code></div>
<div class="box"><div>Check code</div><div class="check">${esc(k.check_code)}</div></div>
<p>Public key: <code>${esc(k.public_key)}</code></p>
<p><strong>To restore:</strong> Hotel Admin → System → Backups → Restore, and type the private key exactly as printed when asked for the backup key.</p>
</body></html>`);
  w.document.close();
  w.focus();
  w.print();
}

export function BackupKeysCard({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [data, setData] = useState<Resp | null>(null);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Holder | null>(null);
  const [testing, setTesting] = useState(false);

  const load = useCallback(async () => {
    try { setData(await api.get<Resp>("/backup-keys/")); } catch { setData(null); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  if (!data || !data.available) return null;
  const shown = data.holders.filter((h) => h.status !== "PENDING");
  const test = data.restore_test;

  return (
    <Card>
      <CardBody className="space-y-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <div className="flex items-center gap-2 text-emphasis">
              {data.encrypted ? <Lock className="size-4" aria-hidden /> : <LockOpen className="size-4" aria-hidden />}
              Backup encryption
              {data.encrypted
                ? <Badge tone="ok" dot>On · {data.active_holders} key holder{data.active_holders === 1 ? "" : "s"}</Badge>
                : <Badge tone="warn" dot>Off</Badge>}
            </div>
            <p className="text-caption text-muted-foreground">
              Backups are encrypted to the hotel’s own keys. Any one key holder can restore; nobody else can read them,
              including the appliance and its supplier. If every key is lost, the backups cannot be read by anyone.
            </p>
          </div>
          {writable && (
            <div className="flex flex-wrap gap-2">
              {data.encrypted && <Button size="sm" variant="outline" onClick={() => setTesting(true)}><ShieldCheck /> Test a key</Button>}
              <Button size="sm" variant={data.encrypted ? "outline" : "primary"} onClick={() => setAdding(true)}><KeyRound /> Add key holder</Button>
            </div>
          )}
        </div>

        {!data.encrypted && (
          <Callout tone="warning" title="Backups are not encrypted">
            Anyone who obtains a backup file can read every guest record in it. Add a key holder to switch encryption on;
            backups taken before that stay unencrypted until the retention sweep removes them.
          </Callout>
        )}
        {data.encrypted && test?.overdue && (
          <Callout tone="warning" title="Restore test overdue">
            {test.last_tested_at ? `No key has been proven since ${formatDate(test.last_tested_at)}.` : "No key has been proven yet."}{" "}
            Ask a key holder to test their key, so a lost key is found now rather than on the day a restore is needed.
          </Callout>
        )}
        {data.encrypted && test && !test.overdue && (
          <p className="text-caption text-muted-foreground">
            {test.last_tested_at ? `Last restore test ${formatDate(test.last_tested_at)}. ` : "No restore test yet. "}
            {test.due_at ? `Next due ${formatDate(test.due_at)} (every ${test.interval_months} months).` : ""}
          </p>
        )}

        {shown.length > 0 && (
          <div className="overflow-x-auto">
            <Table>
              <THead><TR><TH>Key holder</TH><TH>Status</TH><TH>Added</TH><TH>Last proven</TH><TH><span className="sr-only">Actions</span></TH></TR></THead>
              <TBody>
                {shown.map((h) => (
                  <TR key={h.id}>
                    <TD>
                      <div>{h.label}</div>
                      <div className="break-all font-mono text-2xs text-muted-foreground">{h.public_key}</div>
                    </TD>
                    <TD>{h.status === "ACTIVE" ? <Badge tone="ok">Active</Badge> : <Badge tone="neutral">Removed</Badge>}</TD>
                    <TD>{formatDate(h.created_at)}</TD>
                    <TD>{h.last_tested_at ? formatDate(h.last_tested_at) : "—"}</TD>
                    <TD className="text-end">
                      {writable && h.status === "ACTIVE" && (
                        <Button size="sm" variant="ghost" onClick={() => setRemoving(h)} aria-label={`Remove ${h.label}`}>Remove</Button>
                      )}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </div>
        )}
      </CardBody>

      {adding && <AddHolderDialog onClose={() => setAdding(false)} onDone={async (label) => {
        setAdding(false); toast.success("Key holder added", `New backups are encrypted to ${label}’s key too.`); await load();
      }} />}
      {testing && <TestKeyDialog onClose={() => setTesting(false)} onDone={async () => { await load(); }} />}
      <RemoveHolderDialog holder={removing} onClose={() => setRemoving(null)} onDone={async () => {
        setRemoving(null); toast.success("Key holder removed", "New backups are no longer encrypted to that key. Older backups still open with it."); await load();
      }} />
    </Card>
  );
}

function AddHolderDialog({ onClose, onDone }: { onClose: () => void; onDone: (label: string) => void }) {
  const [label, setLabel] = useState("");
  const [ack, setAck] = useState(false);
  const [password, setPassword] = useState("");
  const [key, setKey] = useState<NewKey | null>(null);
  const [saved, setSaved] = useState(false);
  const [check, setCheck] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function create() {
    setBusy(true); setErr(null);
    try {
      setKey(await api.post<NewKey>("/backup-keys/holders", { label, acknowledge_loss: ack, password }));
      setPassword("");
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }
  async function confirm() {
    if (!key) return;
    setBusy(true); setErr(null);
    try {
      await api.post(`/backup-keys/holders/${key.id}/confirm`, { check_code: check });
      onDone(key.label);
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  if (!key) {
    return (
      <DialogForm open onOpenChange={(v) => { if (!v) onClose(); }} title="Add a backup key holder"
        description="The appliance creates a key for this person or place and shows it once. Every new backup is encrypted to it."
        submitLabel="Create key" busy={busy} error={err}
        disabled={label.trim() === "" || !ack || password === ""} onSubmit={create}>
        <Field label="Key holder" required hint="Who or where keeps it, for example “IT manager”, “General manager” or “Hotel safe”.">
          <Input value={label} maxLength={80} onChange={(e) => setLabel(e.target.value)} />
        </Field>
        <label className="flex items-start gap-2 text-sm">
          <input type="checkbox" className="mt-1" checked={ack} onChange={(e) => setAck(e.target.checked)} />
          <span>I understand that if every backup key is lost, the backups cannot be read by anyone — not the hotel, the appliance or its supplier. Keep more than one key holder.</span>
        </label>
        <Field label="Your password" required>
          <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
      </DialogForm>
    );
  }
  return (
    <DialogForm open onOpenChange={(v) => { if (!v) onClose(); }} title={`Save ${key.label}’s key now`}
      description="This is the only time the appliance shows this key. It keeps no copy."
      submitLabel="Confirm" busy={busy} error={err} disabled={!saved || check.trim() === ""} onSubmit={confirm}>
      <Callout tone="warning" title="Shown once">
        Download the key file or print the recovery sheet, and keep it somewhere only authorised people can reach. The check
        code below is printed in both: typing it back proves the key was saved, and only then does encryption rely on it.
      </Callout>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" onClick={() => { downloadText(key.key_file_name, key.key_file); setSaved(true); }}>
          <Download /> Download key file
        </Button>
        <Button type="button" variant="outline" onClick={() => { printSheet(key); setSaved(true); }}>
          <Printer /> Print recovery sheet
        </Button>
      </div>
      <Field label="Check code" required hint="From the key file or the recovery sheet. Within one hour.">
        <Input value={check} onChange={(e) => setCheck(e.target.value)} disabled={!saved} placeholder="XXXX-XXXX" className="font-mono uppercase" />
      </Field>
    </DialogForm>
  );
}

function RemoveHolderDialog({ holder, onClose, onDone }: { holder: Holder | null; onClose: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ConfirmDialog open={holder !== null} onOpenChange={(v) => { if (!v) { setErr(null); onClose(); } }}
      title={`Remove ${holder?.label ?? "this key holder"}?`}
      description="New backups will no longer be encrypted to this key. Backups taken while it was a holder still open with it — they age out under the retention settings."
      confirmLabel="Remove key holder" confirmVariant="danger" busy={busy} error={err}
      requireReason reasonMinLength={4} requirePassword
      onConfirm={async ({ reason, password }) => {
        if (!holder) return;
        setBusy(true); setErr(null);
        try { await api.post(`/backup-keys/holders/${holder.id}/remove`, { reason, password }); onDone(); }
        catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
      }} />
  );
}

function TestKeyDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [identity, setIdentity] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [result, setResult] = useState<{ ok: boolean; holder?: string; backup?: string; detail?: string } | null>(null);
  async function run() {
    setBusy(true); setErr(null); setResult(null);
    try {
      const r = await api.post<{ ok: boolean; holder?: string; backup?: string; detail?: string }>("/backup-keys/test", { identity });
      setResult(r);
      setIdentity("");
      if (r.ok) onDone();
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }
  return (
    <DialogForm open onOpenChange={(v) => { if (!v) onClose(); }} title="Test a backup key"
      description="The appliance opens its newest backup with this key, reads a few bytes and forgets the key. Nothing is restored."
      submitLabel="Test key" busy={busy} error={err} disabled={identity.trim() === ""} onSubmit={run}>
      <BackupKeyInput value={identity} onChange={setIdentity} />
      {result?.ok && <Callout tone="success" title="The key works">{result.holder}’s key opened {result.backup}.</Callout>}
      {result && !result.ok && <Callout tone="danger" title="The key did not open the backup">{result.detail}</Callout>}
    </DialogForm>
  );
}
