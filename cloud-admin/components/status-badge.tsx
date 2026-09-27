import { Badge } from "@/components/ui/badge";
import { activationInfo, connectionInfo, licenseInfo, type StateInfo } from "@/lib/status";
import { cn } from "@/lib/utils";

/** A state as a badge: always the word, the colour only reinforces it. The explanation is the hover title. */
export function StateBadge({ info, className }: { info: StateInfo; className?: string }) {
  return (
    <Badge tone={info.tone} dot className={className}>
      <span title={info.explain || undefined}>{info.label}</span>
    </Badge>
  );
}

export function ActivationBadge({ value, className }: { value: string; className?: string }) {
  return <StateBadge info={activationInfo(value)} className={className} />;
}

export function ConnectionBadge({ value, className }: { value: string; className?: string }) {
  return <StateBadge info={connectionInfo(value)} className={className} />;
}

export function LicenseBadge({ value, className }: { value: string | null | undefined; className?: string }) {
  return <StateBadge info={licenseInfo(value ?? "none")} className={className} />;
}

/** A small label/value pair for fact lists (a <dl>). */
export function Fact({ label, children, className }: { label: React.ReactNode; children: React.ReactNode; className?: string }) {
  return (
    <div className={cn("min-w-0", className)}>
      <dt className="text-caption text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 break-words text-sm text-foreground">{children}</dd>
    </div>
  );
}
