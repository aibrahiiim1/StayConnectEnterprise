"use client";

// THE MODAL THE ADMIN DID NOT HAVE.
//
// Every edit in this product used to open a form INSIDE the page: "Add package" pushed a card above the table,
// "Edit" pushed another one, and the list the operator was editing scrolled out of view underneath. On a long
// screen the form could open entirely off-screen, so the click appeared to do nothing — and with two of them
// open at once there was no way to tell which record a Save applied to.
//
// This is Radix Dialog, so the hard parts are correct rather than approximated: focus moves into the dialog and
// is restored on close, Escape and the overlay dismiss it, the background is inert to the screen reader, and the
// page behind does not scroll.
//
// `DialogForm` exists because nearly every use here is the same shape: a titled form with a footer that has
// Cancel on the left of a single confirming action, an error line above it, and a busy state that disables both.
// Twelve screens hand-rolled that layout in twelve slightly different ways.

import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";
import { ErrorBanner } from "./error-banner";
import { Field, Input } from "./input";

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
          "fixed inset-0 z-50 bg-foreground/45 backdrop-blur-[2px]",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0",
        )}
      />
      {/*
        The positioning is a CENTRED FLEX WRAPPER rather than the usual `top-1/2 -translate-y-1/2`, because
        these dialogs hold real forms: a package editor is taller than a laptop viewport. A translated, fixed
        panel clips its own top and bottom with no way to reach them. This one is bounded to the viewport and
        scrolls INSIDE, so the header and footer stay put and the fields scroll between them.
      */}
      <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto p-4 sm:items-center sm:p-6">
        <DialogPrimitive.Content
          className={cn(
            "relative my-auto flex w-full flex-col rounded-lg border border-border bg-card text-card-foreground shadow-lg",
            "max-h-[calc(100vh-2rem)] sm:max-h-[calc(100vh-3rem)]",
            "data-[state=open]:animate-in data-[state=closed]:animate-out",
            "data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0",
            "data-[state=open]:zoom-in-95 data-[state=closed]:zoom-out-95",
            SIZES[size],
            className,
          )}
          {...props}
        >
          {children}
          <DialogPrimitive.Close
            className={cn(
              "absolute right-3.5 top-3.5 rounded-md p-1 text-muted-foreground",
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
  return <DialogPrimitive.Title className={cn("text-base font-semibold tracking-tight", className)} {...p} />;
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

// The scrolling middle. Anything that can be long goes in here, not in the dialog root.
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

/**
 * DialogForm — a dialog whose body is a form and whose footer is its submit.
 *
 * The submit button lives in the footer but belongs to the form in the body, which is why the form carries an
 * id and the button carries `form={id}`: a footer outside the <form> element cannot submit it otherwise, and
 * moving the footer inside would make it scroll away with the fields.
 */
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

/**
 * ConfirmDialog — replaces `window.confirm` and, more importantly, the `window.prompt` chains.
 *
 * Disabling a package asked for a reason with `window.prompt`, then asked for a password with a SECOND
 * `window.prompt`. Cancelling the second left the operator with no idea whether the first had taken effect, the
 * password was typed into a field that shows its characters, and neither prompt could say what the action would
 * do. All three are properties of the browser dialog, not of the code using it.
 *
 * `requireReason` and `requirePassword` cover the step-up the backend already enforces on consequential routes.
 */
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
  reasonLabel = "Reason",
  reasonPlaceholder,
  requirePassword = false,
  passwordLabel = "Confirm your password",
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
  reasonLabel?: string;
  reasonPlaceholder?: string;
  requirePassword?: boolean;
  passwordLabel?: string;
  onConfirm: (args: { reason: string; password: string }) => void | Promise<void>;
  children?: React.ReactNode;
}) {
  const [reason, setReason] = React.useState("");
  const [password, setPassword] = React.useState("");
  // Real ids, so the label is PROGRAMMATICALLY associated with its input rather than merely sitting above it.
  // The first version rendered the label as a <div>, which looks identical and is invisible to a screen reader
  // and to `getByLabelText` — a control asking for a password with no accessible name.
  const reasonId = React.useId();
  const passwordId = React.useId();

  // Clearing on close rather than on open: a password must not survive in component state after the dialog
  // that collected it has gone.
  React.useEffect(() => {
    if (!open) {
      setReason("");
      setPassword("");
    }
  }, [open]);

  const ready = (!requireReason || reason.trim() !== "") && (!requirePassword || password !== "");

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
      {children}
      {requireReason && (
        <Field label={reasonLabel} htmlFor={reasonId} required>
          <Input
            id={reasonId}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={reasonPlaceholder}
            required
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

/**
 * DetailDialog — a read-only panel for "view this record".
 *
 * Several lists answer "tell me more about this row" by expanding an extra <tr> underneath it, which pushes
 * every following row down and cannot hold more than a few lines. This is the same information with room for it.
 */
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
