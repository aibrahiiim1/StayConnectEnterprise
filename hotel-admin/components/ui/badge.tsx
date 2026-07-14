import { cn } from "@/lib/utils";

type Tone = "default" | "ok" | "warn" | "err" | "info";

export function Badge({
  tone = "default",
  className,
  children,
}: { tone?: Tone; className?: string; children: React.ReactNode }) {
  const tones: Record<Tone, string> = {
    default: "bg-panel2 text-muted border border-border",
    ok:      "bg-[#123422] text-ok border border-[#1e5c3c]",
    warn:    "bg-[#3a2a0e] text-warn border border-[#6b4e1c]",
    err:     "bg-[#3a1418] text-err border border-[#6b2128]",
    info:    "bg-[#14243a] text-brand border border-[#2b4a7e]",
  };
  return (
    <span className={cn("inline-flex items-center px-2 h-5 rounded text-[11px] font-medium", tones[tone], className)}>
      {children}
    </span>
  );
}
