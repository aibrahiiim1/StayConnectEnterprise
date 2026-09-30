// A list of readiness codes as sentences. Shared by Payment methods and Hotel → Room charge so the same code
// reads the same way on both. An unknown code is rendered as the code itself, in a <code> element — the server
// may learn a reason before this console learns its wording, and the operator must still see it.

import { codeWords } from "@/lib/payment-admin";
import { cn } from "@/lib/utils";

export function CodeText({ code }: { code: string }) {
  const words = codeWords(code);
  return words ? <>{words}</> : <code className="font-mono text-xs">{code}</code>;
}

export function CodeList({ codes, className }: { codes: string[]; className?: string }) {
  if (codes.length === 0) return null;
  return (
    <ul className={cn("list-disc space-y-0.5 ps-5 text-sm", className)}>
      {codes.map((c) => (
        <li key={c}><CodeText code={c} /></li>
      ))}
    </ul>
  );
}
