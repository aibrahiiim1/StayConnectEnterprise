"use client";

import { useCallback, useEffect, useState } from "react";
import { Tags, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { Badge, type Tone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox, Field, Input, Select } from "@/components/ui/input";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";

export type Tag = { id: string; name: string; color: Tone; customers: number; appliances: number };
export type TagSubject = { customer_id: string } | { appliance_id: string };

const COLORS: { value: Tone; label: string }[] = [
  { value: "neutral", label: "Grey" },
  { value: "info", label: "Blue" },
  { value: "ok", label: "Green" },
  { value: "warn", label: "Amber" },
  { value: "err", label: "Red" },
  { value: "accent", label: "Brand" },
];

function query(s: TagSubject): string {
  const [k, v] = Object.entries(s)[0];
  return `${k}=${encodeURIComponent(v)}`;
}

export function TagChips({ tags }: { tags: Tag[] }) {
  return (
    <>
      {tags.map((t) => <Badge key={t.id} tone={t.color} size="sm">{t.name}</Badge>)}
    </>
  );
}

export function TagsBar({ subject, what }: { subject: TagSubject; what: string }) {
  const [tags, setTags] = useState<Tag[] | null>(null);
  const [canWrite, setCanWrite] = useState(false);
  const [open, setOpen] = useState(false);
  const key = query(subject);

  const load = useCallback(() => {
    api.get<{ items: Tag[]; can_write: boolean }>(`/cloud/v1/tags/links?${key}`)
      .then((r) => { setTags(r.items ?? []); setCanWrite(!!r.can_write); })
      .catch(() => setTags([]));
  }, [key]);

  useEffect(load, [load]);

  if (!tags) return null;
  if (!tags.length && !canWrite) return null;

  return (
    <div className="flex flex-wrap items-center gap-1.5" aria-label="Tags">
      <TagChips tags={tags} />
      {canWrite && (
        <Button variant="ghost" size="sm" onClick={() => setOpen(true)}>
          <Tags aria-hidden /> {tags.length ? "Edit tags" : "Add tags"}
        </Button>
      )}
      {canWrite && (
        <TagsDialog open={open} onOpenChange={setOpen} subject={subject} what={what} current={tags}
          onSaved={(t) => setTags(t)} />
      )}
    </div>
  );
}

function TagsDialog({ open, onOpenChange, subject, what, current, onSaved }: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  subject: TagSubject;
  what: string;
  current: Tag[];
  onSaved: (t: Tag[]) => void;
}) {
  const toast = useToast();
  const [all, setAll] = useState<Tag[] | null>(null);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [name, setName] = useState("");
  const [color, setColor] = useState<Tone>("neutral");
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState("");

  const loadAll = useCallback(() => {
    api.get<{ items: Tag[] }>("/cloud/v1/tags").then((r) => setAll(r.items ?? [])).catch(setErr);
  }, []);

  useEffect(() => {
    if (!open) return;
    setPicked(new Set(current.map((t) => t.id)));
    setName("");
    setColor("neutral");
    setConfirmDelete(null);
    setErr(null);
    loadAll();
  }, [open, current, loadAll]);

  async function create() {
    if (!name.trim()) return;
    setBusy("create");
    setErr(null);
    try {
      const t = await api.post<Tag>("/cloud/v1/tags", { name, color });
      setAll((a) => [...(a ?? []), t].sort((x, y) => x.name.localeCompare(y.name)));
      setPicked((p) => new Set(p).add(t.id));
      setName("");
    } catch (e) {
      setErr(e);
    } finally {
      setBusy("");
    }
  }

  async function removeTag(id: string) {
    setBusy(id);
    setErr(null);
    try {
      await api.del(`/cloud/v1/tags/${id}`);
      setAll((a) => (a ?? []).filter((t) => t.id !== id));
      setPicked((p) => { const n = new Set(p); n.delete(id); return n; });
      setConfirmDelete(null);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy("");
    }
  }

  async function save() {
    setBusy("save");
    setErr(null);
    try {
      const r = await api.put<{ items: Tag[] }>("/cloud/v1/tags/links", { ...subject, tag_ids: [...picked] });
      onSaved(r.items ?? []);
      onOpenChange(false);
      toast.success("Tags saved");
    } catch (e) {
      setErr(e);
    } finally {
      setBusy("");
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>Tags on this {what}</DialogTitle>
          <DialogDescription>Tags are shared by the whole Central team and can be searched for.</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <ErrorBanner err={err} />
          {all && all.length === 0 && <p className="text-sm text-muted-foreground">No tags yet. Create the first one below.</p>}
          {all && all.length > 0 && (
            <ul className="space-y-2">
              {all.map((t) => (
                <li key={t.id} className="flex items-center justify-between gap-2">
                  <Checkbox
                    label={<Badge tone={t.color} size="sm">{t.name}</Badge>}
                    checked={picked.has(t.id)}
                    onChange={(e) => setPicked((p) => {
                      const n = new Set(p);
                      if (e.target.checked) n.add(t.id); else n.delete(t.id);
                      return n;
                    })}
                  />
                  {confirmDelete === t.id ? (
                    <span className="flex items-center gap-1 text-xs">
                      <span className="text-muted-foreground">
                        Remove from {t.customers + t.appliances} {t.customers + t.appliances === 1 ? "record" : "records"}?
                      </span>
                      <Button variant="ghost" size="sm" onClick={() => setConfirmDelete(null)}>Keep</Button>
                      <Button variant="danger" size="sm" loading={busy === t.id} onClick={() => removeTag(t.id)}>Delete tag</Button>
                    </span>
                  ) : (
                    <Button variant="ghost" size="sm" aria-label={`Delete tag ${t.name} everywhere`} disabled={busy !== ""}
                      onClick={() => setConfirmDelete(t.id)}>
                      <Trash2 aria-hidden />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
          <div className="grid grid-cols-[1fr_auto] items-end gap-2 border-t border-border pt-4">
            <Field label="New tag">
              <Input value={name} maxLength={40} placeholder="For example VIP or Pilot" onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); create(); } }} />
            </Field>
            <Field label="Colour">
              <Select value={color} onChange={(e) => setColor(e.target.value as Tone)} className="w-28">
                {COLORS.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
              </Select>
            </Field>
            <Button className="col-span-2 justify-self-end" size="sm" variant="outline" disabled={!name.trim()}
              loading={busy === "create"} onClick={create}>
              Create tag
            </Button>
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button loading={busy === "save"} disabled={busy !== "" && busy !== "save"} onClick={save}>Save tags</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
