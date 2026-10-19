import * as React from "react";
import { cn } from "@/lib/utils";

// FORM CONTROLS, in one place. `Input` and `Label` kept their signatures; `Select`, `Field` and `Hint` are
// added because pages were writing `className="rounded border px-2 py-1"` by hand — which is how a screen ends
// up with a bordered box and no background colour, invisible against a dark card.
//
// Focus is the ink border PLUS a 2px ring at 60%: the old 20% ring measured 1.5:1 against the card, so the
// border change alone carried the focus state. Placeholders use the full slate (6.2:1 on card), never a
// faded one.
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

// A native <select>, styled. Radix's Select is used where a rich listbox earns its complexity; for a short list
// of fixed options the native control is better on every axis including mobile and keyboard.
export const Select = React.forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  function Select({ className, ...p }, ref) {
    return (
      <select
        ref={ref}
        className={cn(
          CONTROL,
          // The chevron and the fill live in the .select-chevron component class (globals.css): as arbitrary
          // utilities they collided inside tailwind-merge and cost the control its background colour.
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

/**
 * useFieldErrors — an error that belongs to one field is shown under THAT field and moves focus to it.
 *
 * A form whose submit fails because of one value (a client-side check, or a server answer that names the field)
 * used to report it in the banner at the top of the dialog, tied to nothing: a screen-reader user heard the
 * sentence and then had to find which control it meant. Now the caller gives each control `id={f.id("email")}`
 * and its Field `error={f.error("email")}`; `f.fail({ email: "…" })` sets the message and focuses the first field
 * named. Field already wires `aria-invalid` and `aria-describedby` from `error`.
 */
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
      // The asterisk is visual only, so the requirement is announced too. aria-required rather than the native
      // attribute: several forms validate in their own words, and a browser bubble would pre-empt them.
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
