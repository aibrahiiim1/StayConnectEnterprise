"use client";

// Every erasure is written to the appliance and to a required secondary storage before it is reported done;
// until that is verified, erasure requests are refused (guest internet access is never affected).
// Nothing on this card says "copied" or "in step" unless the appliance read the copy back: a Test is a connectivity test only.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/ui/badge";
import { Field, Input, Select, Textarea } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatBytes } from "@/lib/utils";
import { RefreshCw } from "lucide-react";
import { BackupKeyInput } from "./backup-keys-card";
import {
  STORAGE_KINDS, agoWords, kindLabel, ledgerHeadline, mediumLabel, type LedgerStatus, type StorageLocation,
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

const REMOVE_CONFIRMATION = "remove secondary storage";

/** The saved settings, ready to edit; credentials are never sent back, so their fields start empty. */
function fromSettings(s: NonNullable<LedgerStatus["secondary"]["settings"]>): Form {
  const kind = (s.kind === "sftp" || s.kind === "s3" ? s.kind : "dir") as Form["kind"];
  return {
    ...EMPTY, kind, path: s.path ?? "", host: s.host ?? "", port: String(s.port || 22), user: s.user ?? "", dir: s.dir ?? "",
    fingerprint: s.fingerprint ?? "", endpoint: s.endpoint ?? "", region: s.region ?? "", bucket: s.bucket ?? "",
    prefix: s.prefix ?? "", access_key: s.access_key ?? "",
  };
}

function payload(f: Form, keepSecret: boolean): Record<string, unknown> {
  switch (f.kind) {
    case "dir": return { kind: "dir", path: f.path.trim() };
    case "sftp": return {
      kind: "sftp", host: f.host.trim(), port: Number(f.port) || 22, user: f.user.trim(), dir: f.dir.trim(),
      sftp_password: f.password, private_key: f.private_key, fingerprint: f.fingerprint, keep_secret: keepSecret,
    };
    default: return {
      kind: "s3", endpoint: f.endpoint.trim(), region: f.region.trim(), bucket: f.bucket.trim(), prefix: f.prefix.trim(),
      access_key: f.access_key.trim(), secret_key: f.secret_key, keep_secret: keepSecret,
    };
  }
}

function LocationLine({ loc }: { loc?: StorageLocation | null }) {
  if (!loc) return null;
  const space = loc.free_bytes ? ` · ${formatBytes(loc.free_bytes)} free${loc.total_bytes ? ` of ${formatBytes(loc.total_bytes)}` : ""}` : "";
  return (
    <div className="text-muted-foreground">
      Written to <span className="break-all font-mono text-xs">{loc.where}</span>
      {mediumLabel(loc.medium) ? ` · ${mediumLabel(loc.medium)}` : ""}{loc.filesystem ? ` (${loc.filesystem})` : ""}{space}
      {loc.source && loc.medium !== "sftp" && loc.medium !== "s3" && <span className="block break-all font-mono text-2xs">{loc.source}</span>}
    </div>
  );
}

export function LedgerCard({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [status, setStatus] = useState<LedgerStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [form, setForm] = useState<Form>(EMPTY);
  const [editing, setEditing] = useState(false);
  const [editingSaved, setEditingSaved] = useState(false);
  const [probing, setProbing] = useState(false);
  const [probed, setProbed] = useState<string | null>(null);
  const [tested, setTested] = useState<{ text: string; loc?: StorageLocation | null } | null>(null);
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveOpen, setSaveOpen] = useState(false);
  const [saveErr, setSaveErr] = useState<string | null>(null);
  const [notice, setNotice] = useState<{ tone: "success" | "warning"; text: string } | null>(null);
  const [recovering, setRecovering] = useState(false);
  const [recoverErr, setRecoverErr] = useState<string | null>(null);
  const [identity, setIdentity] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [removeErr, setRemoveErr] = useState<string | null>(null);

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
  const sec = status?.secondary;
  // A saved credential is kept when its field is left empty, for the same account on the same server or bucket.
  const keepSecret = editingSaved && !!sec?.configured && form.kind === sec?.settings?.kind;
  const savedSecretHint = (has?: boolean) => (keepSecret && has ? "Saved. Leave empty to keep it, or type a new one to replace it." : undefined);

  function stopEditing() {
    setEditing(false); setEditingSaved(false); setForm(EMPTY); setProbed(null); setTested(null);
  }

  async function probe() {
    setProbing(true); setErr(null);
    try {
      const r = await api.post<{ fingerprint: string }>("/backups/ledger/destination/probe", { host: form.host.trim(), port: Number(form.port) || 22 });
      setProbed(r.fingerprint);
    } catch (e) { setErr(e instanceof ApiError ? e.message : errMsg(e)); } finally { setProbing(false); }
  }

  async function test() {
    setErr(null); setTested(null); setTesting(true);
    try {
      const r = await api.post<{ ok: boolean; message?: string; location?: StorageLocation; existing_ledger?: { entries?: number; same_appliance?: boolean } }>(
        "/backups/ledger/destination/test", payload(form, keepSecret));
      const ex = r.existing_ledger;
      const already = ex && ex.entries ? ` The storage already holds an erasure ledger of ${ex.entries} entries${ex.same_appliance ? " that belongs to this appliance" : ""}.` : "";
      setTested({ text: (r.message ?? "Connectivity test passed. Nothing of the ledger has been copied yet.") + already, loc: r.location });
    } catch (e) { setErr(e instanceof ApiError ? e.message : errMsg(e)); } finally { setTesting(false); }
  }

  async function save({ password }: { password: string }) {
    setSaving(true); setSaveErr(null); setNotice(null);
    try {
      const r = await api.put<{ ok: boolean; message?: string }>("/backups/ledger/destination", { ...payload(form, keepSecret), password });
      setSaving(false); setSaveOpen(false); stopEditing();
      toast.success("Secondary storage in step", r.message ?? "The erasure ledger was copied there and read back.");
      setNotice({ tone: "success", text: r.message ?? "The erasure ledger was copied there and read back." });
      await load(true);
    } catch (e) {
      setSaving(false);
      if (e instanceof ApiError && e.body?.saved) {
        // Saved, but not confirmed in step (or the storage holds newer erasures): never shown as a success.
        setSaveOpen(false); stopEditing();
        setNotice({ tone: "warning", text: e.message });
        await load(true);
        return;
      }
      setSaveErr(e instanceof ApiError ? e.message : errMsg(e));
    }
  }

  async function remove({ password }: { password: string }) {
    setRemoveErr(null);
    try {
      const r = await api.post<{ message?: string }>("/backups/ledger/destination/remove", { password, confirm: REMOVE_CONFIRMATION });
      setRemoving(false); stopEditing();
      setNotice({ tone: "warning", text: r.message ?? "The secondary storage setting was removed. Nothing kept there was deleted." });
      await load(true);
    } catch (e) { setRemoveErr(e instanceof ApiError ? e.message : errMsg(e)); }
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
    <SectionCard
      title="Erasure ledger"
      description="Every erasure is recorded in two places, so a restored backup can never bring an erased guest back."
      actions={
        <Button size="sm" variant="secondary" disabled={refreshing} onClick={async () => { setRefreshing(true); await load(true); setRefreshing(false); }}>
          <RefreshCw aria-hidden /> Check and retry now
        </Button>
      }
      bodyClassName="space-y-4"
    >
        <ErrorBanner err={err} />
        <Callout tone={head.tone === "ok" ? "success" : head.tone === "err" ? "danger" : "warning"} title={head.title}>{head.detail}</Callout>
        {notice && <Callout tone={notice.tone}>{notice.text}</Callout>}

        {status && (
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="rounded-md border border-border bg-surface/60 p-3 text-sm">
              <div className="mb-1 flex items-center gap-2 font-medium">On this appliance <StatusBadge tone={status.primary.present && status.primary.intact ? "ok" : "err"}>{status.primary.present && status.primary.intact ? "Intact" : "Needs repair"}</StatusBadge></div>
              <div className="text-muted-foreground">{status.primary.entries} entries</div>
              {status.primary.error && <div className="text-destructive">{status.primary.error}</div>}
            </div>
            <div className="rounded-md border border-border bg-surface/60 p-3 text-sm">
              <div className="mb-1 flex items-center gap-2 font-medium">Secondary storage
                <StatusBadge tone={!status.secondary.configured ? "err" : status.secondary.in_sync ? "ok" : "warn"}>
                  {!status.secondary.configured ? "Not set up" : status.secondary.in_sync ? "In step (read back)" : "Not in step"}
                </StatusBadge>
              </div>
              <div className="text-muted-foreground">{status.secondary.configured ? `${kindLabel(status.secondary.kind)} — ${status.secondary.describe}` : "Required for erasure"}</div>
              <LocationLine loc={status.secondary.location} />
              {status.secondary.in_sync && status.secondary.head && (
                <div className="text-muted-foreground">
                  Read back at entry {status.secondary.head.seq} · <span className="font-mono text-2xs">{status.secondary.head.hash.slice(0, 16)}…</span> · {status.secondary.head.object}
                </div>
              )}
              {status.secondary.configured && <div className="text-muted-foreground">Last confirmed {agoWords(status.secondary.last_ok)}{status.secondary.encrypted ? " · encrypted" : " · not encrypted (add a backup key holder)"}</div>}
              {status.secondary.error && <div className="text-destructive">{status.secondary.error}</div>}
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
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" onClick={() => {
              if (sec?.settings) { setForm(fromSettings(sec.settings)); setEditingSaved(true); }
              setNotice(null); setEditing(true);
            }}>Edit or replace the secondary storage</Button>
            {sec?.configured && (
              <Button variant="ghost" onClick={() => { setRemoveErr(null); setRemoving(true); }}>Remove…</Button>
            )}
          </div>
        )}

        {showForm && (
          <div className="space-y-3 rounded-md border border-border p-3">
            <Field label="Where the second copy is kept" hint={kindInfo.hint}>
              <Select aria-label="Kind of secondary storage" value={form.kind} onChange={(e) => { setForm({ ...EMPTY, kind: e.target.value as Form["kind"] }); setProbed(null); setTested(null); }}>
                {STORAGE_KINDS.map((k) => <option key={k.value} value={k.value}>{k.label}</option>)}
              </Select>
            </Field>
            {form.kind === "dir" && (
              <Field label="Folder" hint="The mounted folder, for example /mnt/usb/onegate or /mnt/nas/onegate. Mount the disk or share on the appliance first; the appliance refuses a folder on its own disk.">
                <Input value={form.path} onChange={(e) => set("path", e.target.value)} placeholder="/mnt/nas/onegate" />
              </Field>
            )}
            {form.kind === "sftp" && (
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Server"><Input value={form.host} onChange={(e) => { set("host", e.target.value); set("fingerprint", ""); setProbed(null); }} placeholder="nas.example.org" /></Field>
                <Field label="Port"><Input value={form.port} onChange={(e) => set("port", e.target.value)} inputMode="numeric" /></Field>
                <Field label="User name"><Input value={form.user} onChange={(e) => set("user", e.target.value)} autoComplete="off" /></Field>
                <Field label="Folder on the server" hint="Absolute, or relative to the account’s login folder. The folder actually written to is shown after a test.">
                  <Input value={form.dir} onChange={(e) => set("dir", e.target.value)} placeholder="/backups/onegate" />
                </Field>
                <Field label="Password" hint={savedSecretHint(sec?.secrets?.password)}><Input type="password" value={form.password} onChange={(e) => set("password", e.target.value)} autoComplete="new-password" /></Field>
                <Field label="…or SSH private key" hint={savedSecretHint(sec?.secrets?.private_key) ?? "Paste instead of a password (OpenSSH or PEM format)."}><Textarea rows={2} className="font-mono text-xs" value={form.private_key} onChange={(e) => set("private_key", e.target.value)} spellCheck={false} /></Field>
                <div className="sm:col-span-2 space-y-2">
                  <Button variant="secondary" size="sm" disabled={probing || !form.host.trim()} onClick={() => void probe()}>Check the server’s identity</Button>
                  {form.fingerprint && !probed && <p className="text-xs text-muted-foreground">Confirmed key <span className="break-all font-mono">{form.fingerprint}</span>. Check again only if the server’s key has changed.</p>}
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
                <Field label="Endpoint" hint="Leave empty for Amazon S3; for a self-hosted MinIO or NAS, its https address."><Input value={form.endpoint} onChange={(e) => set("endpoint", e.target.value)} placeholder="https://s3.eu-central-003.backblazeb2.com" /></Field>
                <Field label="Region"><Input value={form.region} onChange={(e) => set("region", e.target.value)} placeholder="eu-central-1" /></Field>
                <Field label="Bucket"><Input value={form.bucket} onChange={(e) => set("bucket", e.target.value)} /></Field>
                <Field label="Folder in the bucket" hint="Its own prefix, for example onegate/site-1."><Input value={form.prefix} onChange={(e) => set("prefix", e.target.value)} /></Field>
                <Field label="Access key"><Input value={form.access_key} onChange={(e) => set("access_key", e.target.value)} autoComplete="off" /></Field>
                <Field label="Secret key" hint={savedSecretHint(sec?.secrets?.secret_key)}><Input type="password" value={form.secret_key} onChange={(e) => set("secret_key", e.target.value)} autoComplete="new-password" /></Field>
              </div>
            )}
            {tested && (
              <Callout tone="info" title="Connectivity test passed">
                {tested.text}
                <LocationLine loc={tested.loc} />
              </Callout>
            )}
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" disabled={sftpUnconfirmed || testing} onClick={() => void test()}>Connectivity test</Button>
              <Button variant="primary" disabled={sftpUnconfirmed} onClick={() => { setSaveErr(null); setSaving(false); setSaveOpen(true); }}>Save and copy the ledger…</Button>
              {editing && <Button variant="ghost" onClick={stopEditing}>Cancel</Button>}
            </div>
          </div>
        )}

      <ConfirmDialog
        open={saveOpen}
        onOpenChange={(v) => { if (!v && !saving) setSaveOpen(false); }}
        title={sec?.configured && editingSaved ? "Use these settings for the secondary storage?" : "Use this as the secondary storage?"}
        description="The appliance copies its whole erasure ledger there and reads it back. Only if that succeeds is the setting saved; until then the current setting stays in use. Nothing at the previous storage is deleted."
        confirmLabel="Save and copy the ledger"
        busy={saving}
        error={saveErr}
        requirePassword
        onConfirm={save}
      />
      <ConfirmDialog
        open={removing}
        onOpenChange={(v) => { if (!v) { setRemoving(false); setRemoveErr(null); } }}
        title="Remove the secondary storage?"
        description="The setting is removed; the copy kept there is NOT deleted. From then on erasure requests are refused, because an erasure must be recorded in two independent places, until a secondary storage is set up again. Backups that were copied there stay there."
        confirmLabel="Remove the setting"
        confirmVariant="danger"
        error={removeErr}
        requirePassword
        confirmText={REMOVE_CONFIRMATION}
        onConfirm={remove}
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
      {sec?.history && sec.history.length > 0 && (
        <details className="text-sm">
          <summary className="cursor-pointer text-muted-foreground">Changes to the secondary storage</summary>
          <ul className="mt-1 space-y-0.5 text-xs text-muted-foreground">
            {sec.history.map((h, i) => <li key={i}>{new Date(h.at).toLocaleString()} · {h.action.toLowerCase()} · {kindLabel(h.kind)} · {h.by}</li>)}
          </ul>
        </details>
      )}
    </SectionCard>
  );
}
