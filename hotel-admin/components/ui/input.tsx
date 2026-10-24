import * as React from "react";
import { cn } from "@/lib/utils";

const CONTROL = cn(
  "w-full rounded-md border border-input/70 bg-card text-foreground shadow-control",
  "transition-[border-color,box-shadow] duration-base ease-onegate",
  "placeholder:text-muted-foreground/80",
  "hover:border-input",
  "focus:outline-none focus-visible:outline-none focus:border-ring focus:ring-2 focus:ring-ring/15",
  "disabled:cursor-not-allowed disabled:border-border disabled:bg-surface disabled:text-muted-foreground disabled:shadow-none",
  "[&[readonly]]:bg-surface",
  "aria-[invalid=true]:border-destructive aria-[invalid=true]:focus:ring-destructive/20",
);

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className, ...p }, ref) {
    return <input ref={ref} className={cn(CONTROL, "h-9 px-3 text-sm pointer-coarse:h-11", className)} {...p} />;
  },
);

export const Textarea = React.forwardRef<HTMLTextAreaElement, React.TextareaHTMLAttributes<HTMLTextAreaElement>>(
  function Textarea({ className, ...p }, ref) {
    return <textarea ref={ref} className={cn(CONTROL, "min-h-20 px-3 py-2 text-sm leading-relaxed", className)} {...p} />;
  },
);

export const Select = React.forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  function Select({ className, ...p }, ref) {
    return (
      <select
        ref={ref}
        className={cn(
          CONTROL,
          // The chevron and fill are one component class: separate utilities collide in tailwind-merge.
          "h-9 cursor-pointer appearance-none py-0 ps-3 pe-9 text-sm select-chevron pointer-coarse:h-11",
          className,
        )}
        {...p}
      />
    );
  },
);

/** A native checkbox with its label; the whole row is the hit target. */
export const Checkbox = React.forwardRef<
  HTMLInputElement,
  Omit<React.InputHTMLAttributes<HTMLInputElement>, "type"> & { label?: React.ReactNode; description?: React.ReactNode }
>(function Checkbox({ className, label, description, id, ...p }, ref) {
  const generated = React.useId();
  const cid = id ?? generated;
  const box = (
    <input
      ref={ref}
      id={cid}
      type="checkbox"
      aria-describedby={description ? `${cid}-desc` : undefined}
      className={cn(
        "size-4 shrink-0 cursor-pointer rounded-[4px] border-input accent-[hsl(var(--primary))]",
        "disabled:cursor-not-allowed disabled:opacity-50",
        !label && className,
      )}
      {...p}
    />
  );
  if (!label) return box;
  return (
    <div className={cn("flex items-start gap-2.5", className)}>
      <span className="flex h-5 items-center">{box}</span>
      <div className="min-w-0">
        <label htmlFor={cid} className="cursor-pointer text-sm text-foreground">{label}</label>
        {description && <p id={`${cid}-desc`} className="text-xs text-muted-foreground">{description}</p>}
      </div>
    </div>
  );
});

/** An input with a fixed unit or prefix beside it, e.g. "Mbps" or "https://". */
export function InputGroup({
  prefix, suffix, className, children, id, ...aria
}: {
  prefix?: React.ReactNode;
  suffix?: React.ReactNode;
  className?: string;
  children: React.ReactElement;
  // Field hands these to its direct child; they belong on the input itself, or the label points at nothing.
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean | "true" | "false";
  "aria-required"?: boolean | "true" | "false";
}) {
  const affix = "inline-flex shrink-0 items-center border border-input/70 bg-surface px-3 text-sm text-muted-foreground";
  const inner = children.props as Record<string, unknown> & { className?: string; id?: string };
  const describedBy = [inner["aria-describedby"], aria["aria-describedby"]].filter(Boolean).join(" ") || undefined;
  return (
    <div className={cn("flex items-stretch", className)}>
      {prefix && <span className={cn(affix, "rounded-s-md border-e-0")}>{prefix}</span>}
      {React.cloneElement(children as React.ReactElement<Record<string, unknown>>, {
        id: inner.id ?? id,
        "aria-describedby": describedBy,
        ...(aria["aria-invalid"] !== undefined && inner["aria-invalid"] === undefined ? { "aria-invalid": aria["aria-invalid"] } : null),
        ...(aria["aria-required"] !== undefined && inner["aria-required"] === undefined ? { "aria-required": aria["aria-required"] } : null),
        className: cn(
          inner.className,
          "min-w-0 flex-1",
          prefix && "rounded-s-none",
          suffix && "rounded-e-none",
        ),
      })}
      {suffix && <span className={cn(affix, "rounded-e-md border-s-0")}>{suffix}</span>}
    </div>
  );
}

export function Label({ className, htmlFor, children, ...p }: React.LabelHTMLAttributes<HTMLLabelElement>) {
  return (
    <label htmlFor={htmlFor} className={cn("mb-1.5 block text-label text-foreground", className)} {...p}>
      {children}
    </label>
  );
}

export function Hint({ className, ...p }: React.HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn("mt-1.5 text-xs text-muted-foreground", className)} {...p} />;
}

/** Focus the first invalid field so its error is reachable and announced with the control. */
export function useFieldErrors<K extends string>() {
  const base = React.useId();
  const [errors, setErrors] = React.useState<Partial<Record<K, string>>>({});
  const [focusOn, setFocusOn] = React.useState<{ key: K } | null>(null);
  const id = React.useCallback((k: K) => `${base}-${k}`, [base]);

  React.useEffect(() => {
    if (focusOn) document.getElementById(id(focusOn.key))?.focus();
  }, [focusOn, id]);

  const fail = React.useCallback((errs: Partial<Record<K, string>>) => {
    setErrors(errs);
    const first = (Object.keys(errs) as K[]).find((k) => errs[k]);
    if (first) setFocusOn({ key: first });
    return Boolean(first);
  }, []);
  const clear = React.useCallback(() => setErrors({}), []);
  const error = React.useCallback((k: K) => errors[k], [errors]);

  return { id, error, fail, clear };
}

/** Binds the label, hint and error to the control, generating an id when the caller gave none. */
export function Field({
  label, hint, error, required, optional, htmlFor, className, children,
}: {
  label?: React.ReactNode;
  hint?: React.ReactNode;
  error?: React.ReactNode;
  required?: boolean;
  /** Marks the field "(optional)" — use it instead of asterisks when most fields are required. */
  optional?: boolean;
  htmlFor?: string;
  className?: string;
  children: React.ReactNode;
}) {
  const generated = React.useId();
  const describedBy = `${generated}-desc`;
  const hasDescription = Boolean(error ?? hint);

  // Only a single element can receive the generated id; groups need an explicit htmlFor.
  let control = children;
  let controlId = htmlFor;
  if (React.isValidElement(children)) {
    const childProps = children.props as Record<string, unknown>;
    // An InputGroup passes the id on to its input, so an id the input already has is the one the label must name.
    const groupedId =
      children.type === InputGroup
        ? ((childProps.children as React.ReactElement<{ id?: string }> | undefined)?.props?.id)
        : undefined;
    controlId = htmlFor ?? (childProps.id as string | undefined) ?? groupedId ?? `${generated}-control`;
    control = React.cloneElement(children as React.ReactElement<Record<string, unknown>>, {
      id: controlId,
      ...(hasDescription
        ? { "aria-describedby": [childProps["aria-describedby"], describedBy].filter(Boolean).join(" ") }
        : null),
      ...(error ? { "aria-invalid": true } : null),
      // aria-required announces the rule without a browser popup pre-empting custom validation.
      ...(required && childProps["aria-required"] === undefined ? { "aria-required": true } : null),
    });
  }

  return (
    <div className={cn("min-w-0", className)}>
      {label && (
        <Label htmlFor={controlId}>
          {label}
          {required && <span aria-hidden="true" className="ms-0.5 text-destructive">*</span>}
          {optional && <span className="ms-1 font-normal text-muted-foreground">(optional)</span>}
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

/** A responsive grid of fields: one column on phones, `cols` from the small breakpoint up. */
export function FieldGrid({
  cols = 2, className, children,
}: {
  cols?: 1 | 2 | 3;
  className?: string;
  children: React.ReactNode;
}) {
  const grid = { 1: "", 2: "sm:grid-cols-2", 3: "sm:grid-cols-2 lg:grid-cols-3" }[cols];
  return <div className={cn("grid grid-cols-1 gap-x-4 gap-y-4", grid, className)}>{children}</div>;
}
