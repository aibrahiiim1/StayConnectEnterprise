"use client";

import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { AlertCircle, AlertTriangle, CheckCircle2, Info, X } from "lucide-react";
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
          "fixed inset-0 z-50 bg-scrim/50 backdrop-blur-[1px]",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0 motion-reduce:animate-none",
        )}
      />
      {/* A centered wrapper keeps tall forms scrollable without clipping the header or footer. */}
      <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto p-4 sm:items-center sm:p-6">
        <DialogPrimitive.Content
          className={cn(
            "relative my-auto flex w-full flex-col rounded-xl border border-border bg-card text-card-foreground shadow-overlay",
            // dvh accounts for a mobile browser's collapsing toolbar; vh is the fallback.
            "max-h-[calc(100vh-2rem)] sm:max-h-[calc(100vh-3rem)]",
            "supports-[height:100dvh]:max-h-[calc(100dvh-2rem)] sm:supports-[height:100dvh]:max-h-[calc(100dvh-3rem)]",
            "data-[state=open]:animate-in data-[state=closed]:animate-out",
            "data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0",
            "data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95 motion-reduce:animate-none",
            SIZES[size],
            className,
          )}
          {...props}
        >
          {children}
          <DialogPrimitive.Close
            className={cn(
              "absolute end-3 top-3 inline-flex size-8 items-center justify-center rounded-md text-muted-foreground",
              "pointer-coarse:end-1.5 pointer-coarse:top-1.5 pointer-coarse:size-11",
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
    <div className={cn("shrink-0 space-y-1 px-5 pb-3 pt-4 pe-12", className)} {...p} />
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
  return <div className={cn("min-h-0 flex-1 overflow-y-auto px-5 pb-5 pt-1", className)} {...p} />;
}

export function DialogFooter({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex shrink-0 flex-wrap items-center justify-end gap-2 rounded-b-xl border-t border-border bg-surface/60 px-5 py-3",
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
  disabledReason,
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
  /** Explain disabled submits through aria-describedby so the action is not a dead end. */
  disabledReason?: React.ReactNode;
  extraFooter?: React.ReactNode;
  onSubmit: (e: React.FormEvent<HTMLFormElement>) => void | Promise<void>;
  children: React.ReactNode;
}) {
  const formId = React.useId();
  const reasonId = React.useId();
  const explain = disabled && !busy && Boolean(disabledReason);
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
          {extraFooter && <div className="me-auto flex items-center gap-2">{extraFooter}</div>}
          {explain && (
            <p id={reasonId} className="me-auto text-caption text-muted-foreground">{disabledReason}</p>
          )}
          <Button type="button" variant="ghost" disabled={busy} onClick={() => onOpenChange(false)}>
            {cancelLabel}
          </Button>
          <Button
            type="submit"
            form={formId}
            variant={submitVariant}
            disabled={busy || disabled}
            aria-describedby={explain ? reasonId : undefined}
          >
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
  confirmTextLabel,
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
  /** Require an exact, case-sensitive confirmation for consequential actions. */
  confirmText?: string;
  confirmTextLabel?: React.ReactNode;
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
  const missing = [
    requireReason && reason.trim().length < Math.max(1, reasonMinLength)
      ? reasonOptions
        ? "choose a reason"
        : reasonMinLength > 1
          ? `enter a reason of at least ${reasonMinLength} characters`
          : "enter a reason"
      : null,
    confirmText && typed !== confirmText ? `type ${confirmText} exactly` : null,
    requirePassword && password === "" ? "enter your password" : null,
  ].filter(Boolean) as string[];
  const notReady = missing.length
    ? `To continue, ${missing.length > 1 ? `${missing.slice(0, -1).join(", ")} and ${missing[missing.length - 1]}` : missing[0]}.`
    : undefined;

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
      disabledReason={notReady}
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
          hint={reasonOptions ? "Recorded with the change." : `Recorded with the change. ${reason.length}/${reasonMaxLength}`}
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
        <Field
          label={
            confirmTextLabel ? (
              <>{confirmTextLabel}: <span className="font-mono font-bold">{confirmText}</span></>
            ) : (
              <>Type <span className="font-mono font-bold">{confirmText}</span> to confirm</>
            )
          }
          htmlFor={typedId}
          required
        >
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
        <DialogFooter>
          {footer ?? (
            <Button variant="secondary" onClick={() => onOpenChange(false)}>
              Close
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * A short read-only message that needs acknowledgement: an explanation, a result, a notice. No form, one button.
 * Anything that changes state uses ConfirmDialog.
 */
export function InfoDialog({
  open,
  onOpenChange,
  title,
  description,
  tone = "info",
  closeLabel = "OK",
  size = "sm",
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: React.ReactNode;
  description?: React.ReactNode;
  tone?: "info" | "success" | "warning" | "danger";
  closeLabel?: string;
  size?: keyof typeof SIZES;
  children?: React.ReactNode;
}) {
  const icon = {
    info: <Info className="size-4 shrink-0 text-info" aria-hidden />,
    success: <CheckCircle2 className="size-4 shrink-0 text-success" aria-hidden />,
    warning: <AlertTriangle className="size-4 shrink-0 text-warning" aria-hidden />,
    danger: <AlertCircle className="size-4 shrink-0 text-destructive" aria-hidden />,
  }[tone];
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size={size} role={tone === "danger" || tone === "warning" ? "alertdialog" : "dialog"}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {icon}
            {title}
          </DialogTitle>
          {description ? (
            <DialogDescription>{description}</DialogDescription>
          ) : (
            <DialogDescription className="sr-only">{typeof title === "string" ? title : "Information"}</DialogDescription>
          )}
        </DialogHeader>
        {children && <DialogBody className="space-y-3 text-sm">{children}</DialogBody>}
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="secondary">{closeLabel}</Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
