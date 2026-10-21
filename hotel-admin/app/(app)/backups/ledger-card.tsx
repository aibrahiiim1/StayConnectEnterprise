"use client";

// THE ERASURE LEDGER (GDPR final mission).
//
// Every erasure is written to the appliance AND to a secondary storage the hotel sets up, before it is reported as done. The
// secondary storage is required: until it is set up and verified, erasure requests are refused (nothing else is affected, and
// guest internet access never is). This card sets it up, shows whether both copies are in step, and recovers from the secondary
// storage when the appliance's own copy is behind or gone. The ledger does not depend on the scheduled-backup setting.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { errMsg } from "@/lib/utils";
import { HardDriveUpload, RefreshCw } from "lucide-react";
import { BackupKeyInput } from "./backup-keys-card";
import {
  STORAGE_KINDS, agoWords, kindLabel, ledgerHeadline, type LedgerStatus,
} from "@/lib/ledger-words";

type Form = {
  kind: "dir" | "sftp" | "s3";
  path: string;
  host: string; port: string; user: string; dir: string; password: string; private_key: string; fingerprint: string;
  endpoint: string; region: string; bucket: string; prefix: string; access_key: string; secret_key: string;
};

const EMPTY: Form = {
  kind: "dir", path: "", host: "", port: "22", user: "", dir: "", password: "", private_key: "", fingerprint: "",
  endpoint: "", region: "", bucket: "", prefix: "", access_key: "", secret_key: "",
};

function payload(f: Form): Record<string, unknown> {
  switch (f.kind) {
    case "dir": return { kind: "dir", path: f.path.trim() };
    case "sftp": return {
      kind: "sftp", host: f.host.trim(), port: Number(f.port) || 22, user: f.user.trim(), dir: f.dir.trim(),
      password: f.password, private_key: f.private_key, fingerprint: f.fingerprint,
    };
    default: return {
      kind: "s3", endpoint: f.endpoint.trim(), region: f.region.trim(), bucket: f.bucket.trim(), prefix: f.prefix.trim(),
      access_key: f.access_key.trim(), secret_key: f.secret_key,
    };
  }
}

export function LedgerCard({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [status, setStatus] = useState<LedgerStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [form, setForm] = useState<Form>(EMPTY);
  const [editing, setEditing] = useState(false);
  const [probing, setProbing] = useState(false);
  const [probed, setProbed] = useState<string | null>(null);
  const [tested, setTested] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveOpen, setSaveOpen] = useState(false);
  const [saveErr, setSaveErr] = useState<string | null>(null);
  const [recovering, setRecovering] = useState(false);
  const [recoverErr, setRecoverErr] = useState<string | null>(null);
  const [identity, setIdentity] = useState("");
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async (fresh = false) => {
    try {
      const r = await api.get<LedgerStatus>("/backups/ledger" + (fresh ? "?fresh=1" : ""));
      setStatus(r && r.state ? r : null);
      setErr(null);
    } catch (e) { setErr(errMsg(e)); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const set = <K extends keyof Form>(k: K, v: Form[K]) => { setForm((f) => ({ ...f, [k]: v })); setTested(null); };
  const head = ledgerHeadline(status);
  const kindInfo = STORAGE_KINDS.find((k) => k.value === form.kind)!;
  const showForm = writable && (editing || status?.state === "not_configured");

  async function probe() {
    setProbing(true); setErr(null);
    try {
      const r = await api.post<{ fingerprint: string }>("/backups/ledger/destination/probe", { host: form.host.trim(), port: Number(form.port) || 22 });
      setProbed(r.fingerprint);
    } catch (e) { setErr(e instanceof ApiError ? e.message : errMsg(e)); } finally { setProbing(false); }
  }

  async function test() {
    setErr(null); setTested(null);
    try {
      const r = await api.post<{ ok: boolean; existing_ledger?: { entries?: number; same_appliance?: boolean } }>("/backups/ledger/destination/test", payload(form));
      const ex = r.existing_ledger;
      setTested(ex && ex.entries
        ? `Works. It already holds an erasure ledger of ${ex.entries} entries${ex.same_appliance ? " that belongs to this appliance" : ""}.`
        : "Works. It can be written and read back.");
    } catch (e) { setErr(e instanceof ApiError ? e.message : errMsg(e)); }
  }

  async function save({ password }: { password: string }) {
    setSaving(true); setSaveErr(null);
    try {
      await api.put("/backups/ledger/destination", { ...payload(form), password });
      setSaving(false); setSaveOpen(false); setEditing(false); setForm(EMPTY); setProbed(null); setTested(null);
      toast.success("Secondary storage saved", "The erasure ledger was copied to it and read back.");
      await load(true);
    } catch (e) { setSaveErr(e instanceof ApiError ? e.message : errMsg(e)); setSaving(false); }
  }

  async function recover({ password }: { password: string }) {
    setRecoverErr(null);
    if (!identity.trim()) { setRecoverErr("Paste a backup key holder’s key: the secondary copy is encrypted."); return; }
    try {
      await api.post("/backups/ledger/recover", { identity, password });
      setRecovering(false); setIdentity("");
      toast.success("Ledger recovered", "The appliance now holds every erasure the secondary storage did.");
      await load(true);
    } catch (e) { setRecoverErr(e instanceof ApiError ? e.message : errMsg(e)); }
  }

  const sftpUnconfirmed = form.kind === "sftp" && form.fingerprint === "";

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <CardTitle className="flex items-center gap-2"><HardDriveUpload className="size-4" aria-hidden /> Erasure ledger</CardTitle>
            <CardDescription>
              Every erasure and every answered data request is recorded in two independent places, so a restored backup can never bring an
              erased guest back.
            </CardDescription>
          </div>
          <Button size="sm" variant="outline" disabled={refreshing} onClick={async () => { setRefreshing(true); await load(true); setRefreshing(false); }}>
            <RefreshCw className="size-3.5" aria-hidden /> Check now
          </Button>
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        <ErrorBanner err={err} />
        <Callout tone={head.tone === "ok" ? "success" : head.tone === "err" ? "danger" : "warning"} title={head.title}>{head.detail}</Callout>

        {status && (
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="rounded-md border border-border p-3 text-sm">
              <div className="mb-1 flex items-center gap-2 font-medium">On this appliance <Badge tone={status.primary.present && status.primary.intact ? "ok" : "err"}>{status.primary.present && status.primary.intact ? "intact" : "needs repair"}</Badge></div>
              <div className="text-muted-foreground">{status.primary.entries} entries</div>
              {status.primary.error && <div className="text-danger">{status.primary.error}</div>}
            </div>
            <div className="rounded-md border border-border p-3 text-sm">
              <div className="mb-1 flex items-center gap-2 font-medium">Secondary storage
                <Badge tone={!status.secondary.configured ? "err" : status.secondary.in_sync ? "ok" : "warn"}>
                  {!status.secondary.configured ? "not set up" : status.secondary.in_sync ? "in step" : "not in step"}
                </Badge>
              </div>
              <div className="text-muted-foreground">{status.secondary.configured ? `${kindLabel(status.secondary.kind)} — ${status.secondary.describe}` : "Required"}</div>
              {status.secondary.configured && <div className="text-muted-foreground">Last confirmed {agoWords(status.secondary.last_ok)}{status.secondary.encrypted ? " · encrypted" : " · not encrypted (add a backup key holder)"}</div>}
              {status.secondary.error && <div className="text-danger">{status.secondary.error}</div>}
            </div>
          </div>
        )}
        {status?.problems && status.problems.length > 0 && (
          <ul className="list-disc space-y-0.5 ps-5 text-sm text-muted-foreground">{status.problems.map((p, i) => <li key={i}>{p}</li>)}</ul>
        )}

        {writable && status?.state === "recovery_needed" && (
          <Button variant="primary" onClick={() => { setRecoverErr(null); setRecovering(true); }}>Recover from the secondary storage</Button>
        )}

        {writable && !showForm && status?.state !== "not_configured" && (
          <Button variant="outline" onClick={() => setEditing(true)}>Change the secondary storage</Button>
        )}

        {showForm && (
          <div className="space-y-3 rounded-md border border-border p-3">
            <Field label="Where the second copy is kept" hint={kindInfo.hint}>
              <Select aria-label="Kind of secondary storage" value={form.kind} onChange={(e) => { setForm({ ...EMPTY, kind: e.target.value as Form["kind"] }); setProbed(null); setTested(null); }}>
                {STORAGE_KINDS.map((k) => <option key={k.value} value={k.value}>{k.label}</option>)}
              </Select>
            </Field>
            {form.kind === "dir" && (
              <Field label="Folder" hint="The mounted folder, for example /mnt/ledger. The appliance refuses a folder on its own disk.">
                <Input value={form.path} onChange={(e) => set("path", e.target.value)} placeholder="/mnt/ledger" />
              </Field>
            )}
            {form.kind === "sftp" && (
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Server"><Input value={form.host} onChange={(e) => { set("host", e.target.value); set("fingerprint", ""); setProbed(null); }} placeholder="nas.hotel.example" /></Field>
                <Field label="Port"><Input value={form.port} onChange={(e) => set("port", e.target.value)} inputMode="numeric" /></Field>
                <Field label="User name"><Input value={form.user} onChange={(e) => set("user", e.target.value)} autoComplete="off" /></Field>
                <Field label="Folder on the server"><Input value={form.dir} onChange={(e) => set("dir", e.target.value)} placeholder="/backups/onegate" /></Field>
                <Field label="Password"><Input type="password" value={form.password} onChange={(e) => set("password", e.target.value)} autoComplete="new-password" /></Field>
                <Field label="…or private key" hint="Paste instead of a password."><Textarea rows={2} className="font-mono text-xs" value={form.private_key} onChange={(e) => set("private_key", e.target.value)} spellCheck={false} /></Field>
                <div className="sm:col-span-2 space-y-2">
                  <Button variant="outline" size="sm" disabled={probing || !form.host.trim()} onClick={() => void probe()}>Check the server’s identity</Button>
                  {probed && (
                    <label className="flex items-start gap-2 text-sm">
                      <input type="checkbox" className="mt-1" checked={form.fingerprint === probed} onChange={(e) => set("fingerprint", e.target.checked ? probed : "")} />
                      <span>This is my server. Its key fingerprint is <span className="break-all font-mono text-xs">{probed}</span>; the appliance will refuse any server that presents another.</span>
                    </label>
                  )}
                </div>
              </div>
            )}
            {form.kind === "s3" && (
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Endpoint" hint="Leave empty for Amazon S3."><Input value={form.endpoint} onChange={(e) => set("endpoint", e.target.value)} placeholder="https://s3.eu-central-003.backblazeb2.com" /></Field>
                <Field label="Region"><Input value={form.region} onChange={(e) => set("region", e.target.value)} placeholder="eu-central-1" /></Field>
                <Field label="Bucket"><Input value={form.bucket} onChange={(e) => set("bucket", e.target.value)} /></Field>
                <Field label="Folder in the bucket" hint="Its own prefix, for example onegate/site-1."><Input value={form.prefix} onChange={(e) => set("prefix", e.target.value)} /></Field>
                <Field label="Access key"><Input value={form.access_key} onChange={(e) => set("access_key", e.target.value)} autoComplete="off" /></Field>
                <Field label="Secret key"><Input type="password" value={form.secret_key} onChange={(e) => set("secret_key", e.target.value)} autoComplete="new-password" /></Field>
              </div>
            )}
            {tested && <Callout tone="success">{tested}</Callout>}
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" disabled={sftpUnconfirmed} onClick={() => void test()}>Test</Button>
              <Button variant="primary" disabled={sftpUnconfirmed} onClick={() => { setSaveErr(null); setSaving(false); setSaveOpen(true); }}>Save…</Button>
              {editing && <Button variant="outline" onClick={() => { setEditing(false); setForm(EMPTY); setProbed(null); setTested(null); }}>Cancel</Button>}
            </div>
          </div>
        )}
      </CardBody>

      <ConfirmDialog
        open={saveOpen}
        onOpenChange={(v) => { if (!v && !saving) setSaveOpen(false); }}
        title="Use this as the secondary storage?"
        description="The erasure ledger is copied to it now and read back. From then on every erasure is written there before it is reported as done."
        confirmLabel="Save and copy the ledger"
        busy={saving}
        error={saveErr}
        requirePassword
        onConfirm={save}
      />
      <ConfirmDialog
        open={recovering}
        onOpenChange={(v) => { if (!v) { setRecovering(false); setRecoverErr(null); } }}
        title="Recover the ledger from the secondary storage?"
        description="The secondary storage holds erasures this appliance does not have. They are read with a backup key holder’s key, checked, and applied here."
        confirmLabel="Recover"
        error={recoverErr}
        requirePassword
        onConfirm={recover}
      >
        <BackupKeyInput value={identity} onChange={setIdentity} />
      </ConfirmDialog>
    </Card>
  );
}
