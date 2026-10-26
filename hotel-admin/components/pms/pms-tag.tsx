import { Badge } from "@/components/ui/badge";
import { Hotel } from "lucide-react";

/** The PMS connection a record belongs to. Shown only where the site has several, so a single-PMS site is not cluttered. */
export function PmsTag({ label, retired, show = true }: { label?: string | null; retired?: boolean; show?: boolean }) {
  if (!show || !label) return null;
  return (
    <Badge tone="neutral" size="sm" className="max-w-full">
      <Hotel className="size-3 shrink-0" aria-hidden />
      <span className="truncate">{label}</span>
      {retired ? <span className="text-muted-foreground"> · retired</span> : null}
    </Badge>
  );
}

/** "Room 101 (Aqua Club)" for sentences and accessible names. */
export function roomWithPms(room: string | null | undefined, label?: string | null, multi = true): string {
  const r = room ? `room ${room}` : "room unknown";
  return multi && label ? `${r} at ${label}` : r;
}
