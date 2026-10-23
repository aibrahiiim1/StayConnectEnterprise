"use client";

import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";
import { ErrorBanner } from "./error-banner";
import { Field, Input, Select } from "./input";

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;

const SIZES = {
  sm: "max-w-md",
  md: "max-w-xl",
  lg: "max-w-3xl",
  xl: "max-w-5xl",
} as const;

export function DialogContent({
  size = "md",
  className,
  children,
  ...props
}: React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & { size?: keyof typeof SIZES }) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay
        className={cn(
          "fixed inset-0 z-50 bg-scrim/55 backdrop-blur-[2px]",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0",
          "motion-reduce:animate-none",
        )}
      />
      {/* A centered wrapper keeps tall forms scrollable without clipping the header or footer. */}
      <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto p-4 sm:items-center sm:p-6">
        <DialogPrimitive.Content
          className={cn(
            "relative my-auto flex w-full flex-col rounded-xl border border-border bg-card text-card-foreground shadow-overlay",
            "max-h-[calc(100vh-2rem)] sm:max-h-[calc(100vh-3rem)]",
            "data-[state=open]:animate-in data-[state=closed]:animate-out",
            "data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0",
            "data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95",
            "motion-reduce:animate-none",
            SIZES[size],
            className,
          )}
          {...props}
        >
          {children}
          <DialogPrimitive.Close
            className={cn(
              "absolute right-3 top-3 inline-flex size-8 items-center justify-center rounded-md text-muted-foreground",
              "pointer-coarse:size-11 pointer-coarse:right-1.5 pointer-coarse:top-1.5",
              "transition-colors hover:bg-surface hover:text-foreground",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
            )}
          >
            <X className="size-4" />
            <span className="sr-only">Close</span>
          </DialogPrimitive.Close>
        </DialogPrimitive.Content>
      </div>
    </DialogPrimitive.Portal>
  );
}

export function DialogHeader({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("shrink-0 space-y-1 border-b border-border px-5 py-4 pr-12", className)} {...p} />
  );
}

export function DialogTitle({
  className,
  ...p
}: React.ComponentPropsWithoutRef<typeof DialogPrimitive.Title>) {
  return <DialogPrimitive.Title className={cn("text-headline", className)} {...p} />;
}

export function DialogDescription({
  className,
  ...p
}: React.ComponentPropsWithoutRef<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description
      className={cn("text-sm text-muted-foreground", className)}
      {...p}
    />
  );
}

export function DialogBody({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("min-h-0 flex-1 overflow-y-auto px-5 py-4", className)} {...p} />;
}

export function DialogFooter({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "shrink-0 flex flex-wrap items-center justify-end gap-2 border-t border-border bg-surface/40 px-5 py-3",
        className,
      )}
      {...p}
    />
  );
}

/** The footer submits the body form by ID so it stays visible while fields scroll. */
export function DialogForm({
  open,
  onOpenChange,
  title,
  description,
  size = "md",
  submitLabel = "Save",
  submitVariant = "primary",
  cancelLabel = "Cancel",
  busy = false,
  busyLabel,
  error,
  disabled = false,
  extraFooter,
  onSubmit,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: React.ReactNode;
  description?: React.ReactNode;
  size?: keyof typeof SIZES;
  submitLabel?: string;
  submitVariant?: React.ComponentProps<typeof Button>["variant"];
  cancelLabel?: string;
  busy?: boolean;
  busyLabel?: string;
  error?: unknown;
  disabled?: boolean;
  extraFooter?: React.ReactNode;
  onSubmit: (e: React.FormEvent<HTMLFormElement>) => void | Promise<void>;
  children: React.ReactNode;
}) {
  const formId = React.useId();
  return (
    <Dialog open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <DialogContent size={size}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <DialogBody>
          <ErrorBanner err={error} />
          <form
            id={formId}
            onSubmit={(e) => {
              e.preventDefault();
              void onSubmit(e);
            }}
            className="space-y-4"
          >
            {children}
          </form>
        </DialogBody>
        <DialogFooter>
          {extraFooter && <div className="mr-auto flex items-center gap-2">{extraFooter}</div>}
          <Button type="button" variant="ghost" disabled={busy} onClick={() => onOpenChange(false)}>
            {cancelLabel}
          </Button>
          <Button type="submit" form={formId} variant={submitVariant} disabled={busy || disabled}>
            {busy ? busyLabel ?? `${submitLabel}…` : submitLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Collect reasons and passwords together so cancellation cannot leave the action's state ambiguous. */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "Confirm",
  confirmVariant = "primary",
  busy = false,
  error,
  requireReason = false,
  reasonMinLength = 1,
  reasonLabel = "Reason",
  reasonPlaceholder,
  requirePassword = false,
  passwordLabel = "Confirm your password",
  reasonMaxLength = 500,
  reasonOptions,
  confirmText,
  consequences,
  onConfirm,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: React.ReactNode;
  description?: React.ReactNode;
  confirmLabel?: string;
  confirmVariant?: React.ComponentProps<typeof Button>["variant"];
  busy?: boolean;
  error?: unknown;
  requireReason?: boolean;
  /** Match the server's minimum reason length. */
  reasonMinLength?: number;
  reasonLabel?: string;
  reasonPlaceholder?: string;
  requirePassword?: boolean;
  passwordLabel?: string;
  reasonMaxLength?: number;
  reasonOptions?: { value: string; label: string }[];
  /** Require exact, case-sensitive confirmation for consequential actions. */
  confirmText?: string;
  consequences?: React.ReactNode[];
  onConfirm: (args: { reason: string; password: string }) => void | Promise<void>;
  children?: React.ReactNode;
}) {
  const [reason, setReason] = React.useState("");
  const [password, setPassword] = React.useState("");
  // Real IDs bind labels to inputs for screen readers.
  const reasonId = React.useId();
  const passwordId = React.useId();
  const typedId = React.useId();
  const [typed, setTyped] = React.useState("");

  // Clear the password when the dialog closes.
  React.useEffect(() => {
    if (!open) {
      setReason("");
      setPassword("");
      setTyped("");
    }
  }, [open]);

  const ready =
    (!requireReason || reason.trim().length >= Math.max(1, reasonMinLength)) &&
    (!requirePassword || password !== "") &&
    (!confirmText || typed === confirmText);

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={description}
      size="sm"
      submitLabel={confirmLabel}
      submitVariant={confirmVariant}
      busy={busy}
      error={error}
      disabled={!ready}
      onSubmit={() => onConfirm({ reason: reason.trim(), password })}
    >
      {consequences && consequences.length > 0 && (
        <div className="rounded-md border border-destructive/30 bg-destructive-subtle px-3.5 py-3 text-sm text-destructive-subtle-foreground">
          <ul className="list-disc space-y-1 ps-5">
            {consequences.map((c, i) => (
              <li key={i}>{c}</li>
            ))}
          </ul>
        </div>
      )}
      {children}
      {requireReason && (
        <Field label={reasonLabel} htmlFor={reasonId} required
          hint={reasonOptions ? "Recorded in the activity log." : `Recorded in the activity log. ${reason.length}/${reasonMaxLength}`}
        >
          {reasonOptions ? (
            <Select id={reasonId} value={reason} onChange={(e) => setReason(e.target.value)} required>
              <option value="">Choose a reason…</option>
              {reasonOptions.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          ) : (
            <Input
              id={reasonId}
              value={reason}
              maxLength={reasonMaxLength}
              onChange={(e) => setReason(e.target.value)}
              placeholder={reasonPlaceholder}
              required
            />
          )}
        </Field>
      )}
      {confirmText && (
        <Field label={<>Type <span className="font-mono font-bold">{confirmText}</span> to confirm</>} htmlFor={typedId} required>
          <Input
            id={typedId}
            value={typed}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setTyped(e.target.value)}
            className="font-mono"
          />
        </Field>
      )}
      {requirePassword && (
        <Field label={passwordLabel} htmlFor={passwordId} required>
          <Input
            id={passwordId}
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </Field>
      )}
    </DialogForm>
  );
}

export function DetailDialog({
  open,
  onOpenChange,
  title,
  description,
  size = "lg",
  footer,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: React.ReactNode;
  description?: React.ReactNode;
  size?: keyof typeof SIZES;
  footer?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size={size}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <DialogBody className="space-y-5">{children}</DialogBody>
        {footer ? (
          <DialogFooter>{footer}</DialogFooter>
        ) : (
          <DialogFooter>
            <Button variant="secondary" onClick={() => onOpenChange(false)}>
              Close
            </Button>
          </DialogFooter>
        )}
      </DialogContent>
    </Dialog>
  );
}
