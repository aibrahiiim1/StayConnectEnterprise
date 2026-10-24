"use client";

import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

export const Sheet = DialogPrimitive.Root;
export const SheetTrigger = DialogPrimitive.Trigger;
export const SheetClose = DialogPrimitive.Close;

const WIDTHS = {
  sm: "sm:max-w-md",
  md: "sm:max-w-xl",
  lg: "sm:max-w-3xl",
  xl: "sm:max-w-5xl",
} as const;

export function SheetContent({
  width = "md",
  className,
  children,
  ...props
}: React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & { width?: keyof typeof WIDTHS }) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay
        className={cn(
          "fixed inset-0 z-50 bg-scrim/40",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 motion-reduce:animate-none",
        )}
      />
      <DialogPrimitive.Content
        className={cn(
          "fixed inset-y-0 end-0 z-50 flex h-full w-full flex-col border-s border-border bg-card text-card-foreground shadow-overlay",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=open]:slide-in-from-right data-[state=closed]:slide-out-to-right",
          "rtl:data-[state=open]:slide-in-from-left rtl:data-[state=closed]:slide-out-to-left",
          "data-[state=open]:duration-200 data-[state=closed]:duration-150 motion-reduce:animate-none",
          WIDTHS[width],
          className,
        )}
        {...props}
      >
        {children}
        <DialogPrimitive.Close
          className={cn(
            "absolute end-3 top-3 inline-flex size-8 items-center justify-center rounded-md text-muted-foreground",
            "pointer-coarse:end-1.5 pointer-coarse:top-1.5 pointer-coarse:size-11",
            "transition-colors hover:bg-accent hover:text-foreground",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          )}
        >
          <X className="size-4" />
          <span className="sr-only">Close</span>
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}

export function SheetHeader({
  eyebrow,
  title,
  description,
  badges,
  icon,
  className,
  children,
}: {
  /** A short context line above the title, e.g. the record type ("Network revision"). Sentence case. */
  eyebrow?: React.ReactNode;
  title: React.ReactNode;
  description?: React.ReactNode;
  badges?: React.ReactNode;
  icon?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}) {
  return (
    <div className={cn("shrink-0 space-y-3 border-b border-border px-5 py-4 pe-12", className)}>
      <div className="flex items-start gap-3">
        {icon && (
          <span
            className="mt-0.5 inline-flex size-8 shrink-0 items-center justify-center rounded-md bg-surface text-muted-foreground [&_svg]:size-4"
            aria-hidden
          >
            {icon}
          </span>
        )}
        <div className="min-w-0 space-y-0.5">
          {eyebrow && <div className="text-xs text-muted-foreground">{eyebrow}</div>}
          <DialogPrimitive.Title className="text-headline">{title}</DialogPrimitive.Title>
          {description ? (
            <DialogPrimitive.Description className="text-sm text-muted-foreground">
              {description}
            </DialogPrimitive.Description>
          ) : (
            // Radix expects a Description; an empty visible one would add a gap.
            <DialogPrimitive.Description className="sr-only">{typeof title === "string" ? title : ""}</DialogPrimitive.Description>
          )}
          {badges && <div className="flex flex-wrap items-center gap-1.5 pt-1.5">{badges}</div>}
        </div>
      </div>
      {children}
    </div>
  );
}

export function SheetBody({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("min-h-0 flex-1 space-y-6 overflow-y-auto px-5 py-5", className)} {...p} />;
}

export function SheetFooter({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-border bg-surface/60 px-5 py-3",
        className,
      )}
      {...p}
    />
  );
}

export function SheetSection({
  title,
  description,
  actions,
  className,
  children,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}) {
  return (
    <section className={cn("space-y-2.5", className)}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 space-y-0.5">
          <h3 className="text-label text-foreground">{title}</h3>
          {description && <p className="text-xs text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  );
}
