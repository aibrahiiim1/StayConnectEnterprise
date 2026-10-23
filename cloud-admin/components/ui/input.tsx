import * as React from "react";
import { cn } from "@/lib/utils";

const CONTROL = cn(
  "w-full rounded-md border border-input bg-card text-foreground",
  "transition-[border-color,box-shadow] duration-base ease-onegate",
  "placeholder:text-muted-foreground",
  "focus:outline-none focus-visible:outline-none focus:border-foreground focus:ring-2 focus:ring-ring/60",
  "disabled:cursor-not-allowed disabled:bg-surface disabled:text-muted-foreground",
  "aria-[invalid=true]:border-destructive aria-[invalid=true]:ring-destructive/25",
  "aria-[invalid=true]:focus:ring-destructive/60",
);

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className, ...p }, ref) {
    return <input ref={ref} className={cn(CONTROL, "h-10 px-3 text-sm", className)} {...p} />;
  },
);

export const Select = React.forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  function Select({ className, ...p }, ref) {
    return (
      <select
        ref={ref}
        className={cn(
          CONTROL,
          // Keep chevron and fill together because tailwind-merge collides on separate utilities.
          "h-10 cursor-pointer appearance-none py-0 ps-3 pe-9 text-sm select-chevron",
          className,
        )}
        {...p}
      />
    );
  },
);

export function Label({ className, htmlFor, children, ...p }: React.LabelHTMLAttributes<HTMLLabelElement>) {
  return (
    <label
      htmlFor={htmlFor}
      className={cn("mb-1.5 block text-label text-foreground", className)}
      {...p}
    >
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

/** Generate an ID when needed so labels, hints, and errors remain bound to the control. */
export function Field({
  label, hint, error, required, htmlFor, className, children,
}: {
  label?: React.ReactNode;
  hint?: React.ReactNode;
  error?: React.ReactNode;
  required?: boolean;
  htmlFor?: string;
  className?: string;
  children: React.ReactNode;
}) {
  const generated = React.useId();
  const describedBy = `${generated}-desc`;
  const hasDescription = Boolean(error ?? hint);

  // Only a single element can receive the generated ID; groups need an explicit htmlFor.
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
      // aria-required announces the rule without a browser popup pre-empting custom validation.
      ...(required ? { "aria-required": true } : null),
    });
  }

  return (
    <div className={cn("min-w-0", className)}>
      {label && (
        <Label htmlFor={controlId}>
          {label}
          {required && <span className="ms-0.5 text-destructive" aria-hidden>*</span>}
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
