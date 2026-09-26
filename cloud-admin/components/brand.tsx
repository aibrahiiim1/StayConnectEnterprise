import { cn } from "@/lib/utils";

/*
  THE ONEGATE WORDMARK. "One" is set bold in a green linear gradient; "Gate" is set bold in solid black. It is
  type, not an image, so the appliance ships no logo file and the mark renders crisply at every size.

  "Gate" stays black on every surface. Where the surface is dark (the sidebar, the sign-in panel), the wordmark
  sits on a light tile rather than turning "Gate" white -- the black half is part of the mark.

  The company behind the product is Semantics; the product is OneGate (design-system/README.md, "Brand").
*/

const ONE_GRADIENT = "linear-gradient(95deg, #0b7a3b 0%, #149c4a 38%, #3cc05a 70%, #8fdc5f 100%)";

export function OneGateWordmark({
  className,
  as: Tag = "span",
}: {
  className?: string;
  as?: "span" | "div" | "h1";
}) {
  return (
    <Tag
      className={cn("inline-flex items-baseline font-extrabold leading-none tracking-[-0.03em]", className)}
      aria-label="OneGate"
    >
      <span
        aria-hidden
        style={{ backgroundImage: ONE_GRADIENT, WebkitBackgroundClip: "text", backgroundClip: "text", color: "transparent" }}
      >
        One
      </span>
      <span aria-hidden style={{ color: "#0a0a0a" }}>
        Gate
      </span>
    </Tag>
  );
}

/** The compact mark for tight spaces (collapsed rail, favicon-sized): the gradient "O" of "One". */
export function OneGateMark({ className, title }: { className?: string; title?: string }) {
  const id = "onegate-o";
  return (
    <svg
      viewBox="0 0 24 24"
      className={cn("size-5", className)}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
    >
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#0b7a3b" />
          <stop offset="0.5" stopColor="#1fae4f" />
          <stop offset="1" stopColor="#8fdc5f" />
        </linearGradient>
      </defs>
      <circle cx="12" cy="12" r="7.6" fill="none" stroke={`url(#${id})`} strokeWidth="4.2" />
    </svg>
  );
}

/** The wordmark in its tile, with the product line beside it. Same props as the lockup it replaces. */
export function OneGateLockup({
  product,
  collapsed = false,
  inverse = false,
  className,
}: {
  product: string;
  collapsed?: boolean;
  inverse?: boolean;
  className?: string;
}) {
  if (collapsed) {
    return (
      <span
        className={cn(
          "flex size-9 shrink-0 items-center justify-center rounded-lg bg-white shadow-control ring-1 ring-black/5",
          className,
        )}
      >
        <OneGateMark className="size-6" title="OneGate" />
      </span>
    );
  }
  return (
    <div className={cn("flex min-w-0 items-center gap-2.5", className)}>
      <span className="inline-flex shrink-0 items-center rounded-lg bg-white px-2.5 py-1.5 shadow-control ring-1 ring-black/5">
        <OneGateWordmark className="text-[1.0625rem]" />
      </span>
      <div
        className={cn(
          "min-w-0 truncate text-nano uppercase tracking-[0.12em]",
          inverse ? "text-sidebar-muted" : "text-muted-foreground",
        )}
      >
        {product}
      </div>
    </div>
  );
}

/** A one-line company attribution for footers. */
export function BySemantics({ className }: { className?: string }) {
  return <span className={cn("text-caption", className)}>OneGate by Semantics</span>;
}
