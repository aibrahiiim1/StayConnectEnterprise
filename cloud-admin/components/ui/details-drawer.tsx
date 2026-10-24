"use client";

import * as React from "react";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader } from "./sheet";
import { ErrorBanner } from "./error-banner";
import { Skeleton } from "./misc";

/**
 * The right-side drawer for SECONDARY detail: a revision, a version, a change-history or audit entry, a record's full
 * attributes. The page keeps its context behind it. Header (eyebrow · title · badges), a scrollable body, and an
 * optional footer for the record's actions. Use DetailsSection inside the body to group fields.
 *
 * Controlled: pass `open`/`onOpenChange`, typically `open={selected !== null}`.
 */
export function DetailsDrawer({
  open,
  onOpenChange,
  title,
  eyebrow,
  description,
  badges,
  icon,
  actions,
  width = "md",
  loading = false,
  error,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: React.ReactNode;
  /** What kind of record this is, e.g. "Configuration revision". */
  eyebrow?: React.ReactNode;
  description?: React.ReactNode;
  badges?: React.ReactNode;
  icon?: React.ReactNode;
  /** Footer buttons, e.g. "Roll back to this revision". Close is always available from the header. */
  actions?: React.ReactNode;
  width?: "sm" | "md" | "lg" | "xl";
  loading?: boolean;
  error?: unknown;
  children?: React.ReactNode;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent width={width}>
        <SheetHeader eyebrow={eyebrow} title={title} description={description} badges={badges} icon={icon} />
        <SheetBody aria-busy={loading || undefined}>
          <ErrorBanner err={error} className="mb-0" />
          {loading ? (
            <div className="space-y-3" aria-live="polite">
              <span className="sr-only">Loading</span>
              <Skeleton className="h-4 w-2/3" />
              <Skeleton className="h-4 w-1/2" />
              <Skeleton className="h-24 w-full" />
            </div>
          ) : (
            children
          )}
        </SheetBody>
        {actions && <SheetFooter>{actions}</SheetFooter>}
      </SheetContent>
    </Sheet>
  );
}

export { SheetSection as DetailsSection } from "./sheet";
