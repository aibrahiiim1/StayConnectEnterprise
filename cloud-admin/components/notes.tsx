"use client";

import { useCallback, useEffect, useState } from "react";
import { Pin, PinOff } from "lucide-react";
import { api } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Checkbox, Textarea } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/misc";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { useToast } from "@/components/ui/toast";
import { ago, formatDateTime } from "@/lib/status";

export type NoteSubject = { customer_id: string } | { site_id: string } | { appliance_id: string };

export type Note = {
  id: string;
  body: string;
  pinned: boolean;
  author_id?: string | null;
  author_name?: string | null;
  author_email?: string | null;
  created_at: string;
  updated_at: string;
  edited: boolean;
  can_edit: boolean;
  can_delete: boolean;
};

const MAX = 4000;

function query(s: NoteSubject): string {
  const [k, v] = Object.entries(s)[0];
  return `${k}=${encodeURIComponent(v)}`;
}

function author(n: Note): string {
  if (!n.author_id || (!n.author_name && !n.author_email)) return "Removed member";
  return n.author_name || n.author_email || "Removed member";
}

export function NotesCard({ subject, what }: { subject: NoteSubject; what: string }) {
  const toast = useToast();
  const [notes, setNotes] = useState<Note[] | null>(null);
  const [canWrite, setCanWrite] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [draft, setDraft] = useState("");
  const [pin, setPin] = useState(false);
  const [busy, setBusy] = useState<string>("");
  const [editing, setEditing] = useState<{ id: string; body: string } | null>(null);
  const [removing, setRemoving] = useState<Note | null>(null);
  const key = query(subject);

  const load = useCallback(() => {
    api.get<{ items: Note[]; can_write: boolean }>(`/cloud/v1/notes?${key}`)
      .then((r) => { setNotes(r.items ?? []); setCanWrite(!!r.can_write); setErr(null); })
      .catch(setErr);
  }, [key]);

  useEffect(load, [load]);

  async function add() {
    if (!draft.trim()) return;
    setBusy("add");
    setErr(null);
    try {
      await api.post<Note>("/cloud/v1/notes", { ...subject, body: draft, pinned: pin });
      setDraft("");
      setPin(false);
      load();
      toast.success("Note saved");
    } catch (e) {
      setErr(e);
    } finally {
      setBusy("");
    }
  }

  async function saveEdit() {
    if (!editing || !editing.body.trim()) return;
    setBusy(editing.id);
    setErr(null);
    try {
      await api.patch<Note>(`/cloud/v1/notes/${editing.id}`, { body: editing.body });
      setEditing(null);
      load();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy("");
    }
  }

  async function togglePin(n: Note) {
    setBusy(n.id);
    setErr(null);
    try {
      await api.patch<Note>(`/cloud/v1/notes/${n.id}`, { pinned: !n.pinned });
      load();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy("");
    }
  }

  async function remove() {
    if (!removing) return;
    setBusy(removing.id);
    try {
      await api.del(`/cloud/v1/notes/${removing.id}`);
      setRemoving(null);
      load();
      toast.success("Note deleted");
    } catch (e) {
      setErr(e);
      setRemoving(null);
    } finally {
      setBusy("");
    }
  }

  const count = notes?.length ?? 0;

  return (
    <SectionCard
      title={count ? `Notes (${count})` : "Notes"}
      description={`What the Central team knows about this ${what}. Only the Central team sees notes.`}
      bodyClassName="space-y-4"
    >
      <ErrorBanner err={err} />
      {canWrite && (
        <div className="space-y-2">
          <Textarea
            aria-label={`New note on this ${what}`}
            value={draft}
            maxLength={MAX}
            rows={3}
            placeholder="For example: called the front office, the WAN cable is being replaced on Monday."
            onChange={(e) => setDraft(e.target.value)}
          />
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Checkbox label="Pin to the top" checked={pin} onChange={(e) => setPin(e.target.checked)} />
            <div className="flex items-center gap-3">
              <span className="text-xs text-muted-foreground">Never write guest names or details.</span>
              <Button size="sm" disabled={!draft.trim()} loading={busy === "add"} onClick={add}>Add note</Button>
            </div>
          </div>
        </div>
      )}
      {!notes && !err && <Skeleton className="h-16" />}
      {notes && notes.length === 0 && (
        <p className="text-sm text-muted-foreground">No notes yet.</p>
      )}
      {notes && notes.length > 0 && (
        <ul className="divide-y divide-border">
          {notes.map((n) => (
            <li key={n.id} className="space-y-1.5 py-3 first:pt-0 last:pb-0">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                {n.pinned && <Badge size="sm" tone="accent">Pinned</Badge>}
                <span className="font-medium text-foreground">{author(n)}</span>
                <time dateTime={n.created_at} title={formatDateTime(n.created_at)}>{ago(n.created_at)}</time>
                {n.edited && <span title={`Edited ${formatDateTime(n.updated_at)}`}>(edited)</span>}
                <span className="ms-auto flex items-center gap-1">
                  {n.can_delete && (
                    <Button variant="ghost" size="sm" disabled={busy !== ""} onClick={() => togglePin(n)}
                      aria-label={n.pinned ? "Unpin note" : "Pin note"}>
                      {n.pinned ? <PinOff aria-hidden /> : <Pin aria-hidden />}
                    </Button>
                  )}
                  {n.can_edit && editing?.id !== n.id && (
                    <Button variant="ghost" size="sm" disabled={busy !== ""} onClick={() => setEditing({ id: n.id, body: n.body })}>Edit</Button>
                  )}
                  {n.can_delete && (
                    <Button variant="ghost" size="sm" disabled={busy !== ""} onClick={() => setRemoving(n)}>Delete</Button>
                  )}
                </span>
              </div>
              {editing?.id === n.id ? (
                <div className="space-y-2">
                  <Textarea aria-label="Edit note" value={editing.body} maxLength={MAX} rows={3}
                    onChange={(e) => setEditing({ id: n.id, body: e.target.value })} />
                  <div className="flex justify-end gap-2">
                    <Button variant="ghost" size="sm" onClick={() => setEditing(null)}>Cancel</Button>
                    <Button size="sm" disabled={!editing.body.trim() || editing.body === n.body} loading={busy === n.id} onClick={saveEdit}>Save</Button>
                  </div>
                </div>
              ) : (
                <p className="whitespace-pre-wrap break-words text-sm">{n.body}</p>
              )}
            </li>
          ))}
        </ul>
      )}
      <ConfirmDialog
        open={!!removing}
        onOpenChange={(o) => { if (!o) setRemoving(null); }}
        title="Delete this note?"
        description="It is removed for the whole team and cannot be brought back."
        confirmLabel="Delete note"
        confirmVariant="danger"
        busy={!!removing && busy === removing.id}
        onConfirm={remove}
      />
    </SectionCard>
  );
}
