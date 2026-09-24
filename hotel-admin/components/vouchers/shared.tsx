"use client";

// Small pieces every voucher tab uses: a human timestamp with the exact time on hover, the status badge, and
// the wording for a refused step-up.

import * as React from "react";
import { ApiError } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Tooltip } from "@/components/ui/tooltip";
import {
  absoluteTime,
  relativeTime,
  STATUS_EXPLAIN,
  STATUS_TONE,
  STATUS_WORDS,
  type EffectiveStatus,
} from "@/lib/api/vouchers";

/** "3 days ago", with the exact local time in a tooltip and a machine-readable dateTime. Never raw ISO. */
export function When({ iso, prefix, className }: { iso: string | null | undefined; prefix?: string; className?: string }) {
  if (!iso) return <span className="text-muted-foreground">—</span>;
  const abs = absoluteTime(iso);
  return (
    <Tooltip content={abs}>
      <time dateTime={iso} title={abs} tabIndex={0} className={className}>
        {prefix ? `${prefix} ` : ""}
        {relativeTime(iso)}
      </time>
    </Tooltip>
  );
}

export function StatusBadge({ status, plain = false }: { status: EffectiveStatus; plain?: boolean }) {
  // `plain` where the explanation is already printed beside the badge (a sheet header): a focusable tooltip
  // there would take the sheet's initial focus and swallow the first Escape.
  if (plain)
    return (
      <Badge tone={STATUS_TONE[status]} dot>
        {STATUS_WORDS[status]}
      </Badge>
    );
  return (
    <Tooltip content={STATUS_EXPLAIN[status]}>
      <span tabIndex={0} className="inline-flex rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
        <Badge tone={STATUS_TONE[status]} dot>
          {STATUS_WORDS[status]}
        </Badge>
      </span>
    </Tooltip>
  );
}

/**
 * What a refused step-up means, in words. The server answers 401 `reauth_required` for a wrong password and
 * 400 `reason_required` for a missing or unbounded reason; in both cases NOTHING happened, and the operator
 * should be told that rather than shown a status code.
 */
export function stepUpError(e: unknown, nothingHappened: string): Error {
  if (e instanceof ApiError) {
    if (e.code === "reauth_required") return new Error(`That password is not correct. ${nothingHappened}`);
    if (e.code === "reason_required") return new Error(`A reason of 4 to 500 characters is required. ${nothingHappened}`);
  }
  return e instanceof Error ? e : new Error(String(e));
}

/** A reason the server will accept: 4 to 500 characters after trimming. Checked before sending. */
export function reasonProblem(reason: string): string | null {
  const n = reason.trim().length;
  if (n < 4) return "Give a reason of at least 4 characters. It is recorded with your name.";
  if (n > 500) return "Keep the reason under 500 characters.";
  return null;
}
