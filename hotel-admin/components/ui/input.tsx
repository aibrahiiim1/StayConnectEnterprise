import * as React from "react";
import { cn } from "@/lib/utils";

// FORM CONTROLS, in one place. `Input` and `Label` kept their signatures; `Select`, `Textarea`, `Field` and
// `Hint` are added because pages were writing `className="rounded border px-2 py-1"` by hand — which is how a
// screen ends up with a bordered box and no background colour, invisible against a dark card.

const CONTROL = cn(
  "w-full rounded-md border border-input bg-card text-foreground shadow-xs",
  "transition-[border-color,box-shadow] duration-150",
  "placeholder:text-muted-foreground/70",
  "focus:outline-none focus-visible:outline-none focus:border-ring focus:ring-2 focus:ring-ring/25",
  "disabled:cursor-not-allowed disabled:bg-surface disabled:text-muted-foreground",
  "aria-[invalid=true]:border-destructive aria-[invalid=true]:ring-destructive/25",
);

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className, ...p }, ref) {
    return <input ref={ref} className={cn(CONTROL, "h-9 px-3 text-sm", className)} {...p} />;
  },
);

export const Textarea = React.forwardRef<HTMLTextAreaElement, React.TextareaHTMLAttributes<HTMLTextAreaElement>>(
  function Textarea({ className, ...p }, ref) {
    return <textarea ref={ref} className={cn(CONTROL, "min-h-20 px-3 py-2 text-sm", className)} {...p} />;
  },
);

// A native <select>, styled. Radix's Select is used where a rich listbox earns its complexity; for a short list
// of fixed options the native control is better on every axis including mobile and keyboard.
export const Select = React.forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  function Select({ className, ...p }, ref) {
    return (
      <select
        ref={ref}
        className={cn(
          CONTROL,
          "h-9 cursor-pointer appearance-none bg-no-repeat py-0 pl-3 pr-9 text-sm",
          // The chevron is an inline data-URI so it needs no asset and no extra element, and `currentColor`
          // cannot be used in a background image — hence the two theme-matched strokes below.
          "bg-[length:16px] bg-[right_0.625rem_center]",
          "bg-[url(\"data:image/svg+xml;charset=utf-8,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%2364748b' stroke-width='2' stroke-linecap='round'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E\")]",
          "dark:bg-[url(\"data:image/svg+xml;charset=utf-8,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%2394a3b8' stroke-width='2' stroke-linecap='round'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E\")]",
          className,
        )}
        {...p}
      />
    );
  },
);

export function Label({ className, ...p }: React.LabelHTMLAttributes<HTMLLabelElement>) {
  return (
    <label
      className={cn("mb-1.5 block text-xs font-medium text-muted-foreground", className)}
      {...p}
    />
  );
}

export function Hint({ className, ...p }: React.HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn("mt-1.5 text-xs text-muted-foreground", className)} {...p} />;
}

/**
 * Field wires a label, its control and its hint together — and it BINDS them, which is the whole point.
 *
 * The hand-written forms in this app mostly wrapped the input inside the label. That works for a mouse click and
 * loses everything else: the control has no accessible name to announce, `getByLabelText` cannot find it, and a
 * hint sitting under the input is not associated with it at all.
 *
 * So the id is generated here and injected into the child when the child does not already carry one. That is the
 * detail worth spelling out: a `Field` whose caller forgot to pass `htmlFor` used to render a label pointing at
 * nothing, which is indistinguishable from a correct one on screen. Now it cannot happen — the caller writes
 * `<Field label="…"><Input /></Field>` and the binding is not theirs to get wrong.
 *
 * `aria-describedby` carries the hint or the error, so the explanation is announced with the field rather than
 * being visual-only. `aria-invalid` follows `error` for the same reason.
 */
export function Field({
  label, hint, error, required, htmlFor, className, children,
}: {
  label?: React.ReactNode;
  hint?: React.ReactNode;
  error?: React.ReactNode;
  required?: boolean;
  /** Pass this only when the control's id is fixed by something else; otherwise one is generated. */
  htmlFor?: string;
  className?: string;
  children: React.ReactNode;
}) {
  const generated = React.useId();
  const describedBy = `${generated}-desc`;
  const hasDescription = Boolean(error ?? hint);

  // A single element child is the normal case and is the one that can be bound. Anything else (a fragment, a
  // composed group of controls) is rendered untouched, and the caller keeps the option of passing `htmlFor`.
  let control = children;
  let controlId = htmlFor;
  if (React.isValidElement(children)) {
    const childProps = children.props as Record<string, unknown>;
    controlId = htmlFor ?? (childProps.id as string | undefined) ?? `${generated}-control`;
    control = React.cloneElement(children as React.ReactElement<Record<string, unknown>>, {
      id: controlId,
      ...(hasDescription
        ? { "aria-describedby": [childProps["aria-describedby"], describedBy].filter(Boolean).join(" ") }
        : null),
      ...(error ? { "aria-invalid": true } : null),
    });
  }

  return (
    <div className={cn("min-w-0", className)}>
      {label && (
        <Label htmlFor={controlId}>
          {label}
          {required && <span className="ml-0.5 text-destructive">*</span>}
        </Label>
      )}
      {control}
      {error ? (
        <p id={describedBy} className="mt-1.5 text-xs text-destructive">{error}</p>
      ) : (
        hint && <Hint id={describedBy}>{hint}</Hint>
      )}
    </div>
  );
}
