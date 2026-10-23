"use client";

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

export function When({ iso, prefix, className }: { iso: string | null | undefined; prefix?: string; className?: string }) {
  if (!iso) return <span className="text-muted-foreground">—</span>;
  const abs = absoluteTime(iso);
  return (
    <Tooltip content={abs}>
      {/* Keep tooltip details reachable by keyboard. */}
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
      <time dateTime={iso} title={abs} tabIndex={0} className={className}>
        {prefix ? `${prefix} ` : ""}
        {relativeTime(iso)}
      </time>
    </Tooltip>
  );
}

export function StatusBadge({ status, plain = false }: { status: EffectiveStatus; plain?: boolean }) {
  // Plain badges avoid an initial-focus tooltip that would swallow Escape.
  if (plain)
    return (
      <Badge tone={STATUS_TONE[status]} dot>
        {STATUS_WORDS[status]}
      </Badge>
    );
  return (
    <Tooltip content={STATUS_EXPLAIN[status]}>
      {/* Keep tooltip details reachable by keyboard. */}
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
      <span tabIndex={0} className="inline-flex rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
        <Badge tone={STATUS_TONE[status]} dot>
          {STATUS_WORDS[status]}
        </Badge>
      </span>
    </Tooltip>
  );
}

/** Explain that a refused step-up made no change, rather than showing only a status code. */
export function stepUpError(e: unknown, nothingHappened: string): Error {
  if (e instanceof ApiError) {
    if (e.code === "reauth_required") return new Error(`That password is not correct. ${nothingHappened}`);
    if (e.code === "reason_required") return new Error(`A reason of 4 to 500 characters is required. ${nothingHappened}`);
  }
  return e instanceof Error ? e : new Error(String(e));
}

/** Server reasons require 4–500 characters after trimming. */
export function reasonProblem(reason: string): string | null {
  const n = reason.trim().length;
  if (n < 4) return "Give a reason of at least 4 characters. It is recorded with your name.";
  if (n > 500) return "Keep the reason under 500 characters.";
  return null;
}
