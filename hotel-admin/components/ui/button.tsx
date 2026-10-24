"use client";
import * as React from "react";
import Link from "next/link";
import { cva, type VariantProps } from "class-variance-authority";
import { Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  cn(
    "relative inline-flex shrink-0 select-none items-center justify-center gap-1.5 whitespace-nowrap rounded-md font-medium",
    "transition-[background-color,border-color,color,box-shadow] duration-press ease-onegate",
    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    "disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50",
    "[&_svg]:pointer-events-none [&_svg]:shrink-0",
  ),
  {
    variants: {
      variant: {
        primary:
          "bg-primary text-primary-foreground shadow-control hover:bg-primary-hover active:bg-primary-press active:shadow-control-press",
        secondary:
          "border border-border-strong bg-card text-foreground shadow-control hover:bg-accent hover:border-input/60 active:shadow-control-press",
        outline: "border border-border-strong bg-transparent text-foreground hover:bg-accent active:shadow-control-press",
        ghost: "bg-transparent text-muted-foreground hover:bg-accent hover:text-foreground",
        subtle: "bg-accent text-foreground hover:bg-border/80",
        danger:
          "bg-destructive text-destructive-foreground shadow-control hover:bg-destructive/90 active:shadow-control-press",
        "danger-outline":
          "border border-destructive/40 bg-card text-destructive shadow-control hover:bg-destructive-subtle hover:border-destructive/60",
        link: "h-auto bg-transparent px-0 text-primary underline-offset-4 hover:underline",
      },
      size: {
        // Coarse pointers get a 44px touch target without changing desktop density.
        xs: "h-7 px-2 text-xs [&_svg]:size-3.5 pointer-coarse:min-h-11",
        sm: "h-8 px-2.5 text-label [&_svg]:size-4 pointer-coarse:min-h-11",
        md: "h-9 px-3.5 text-label [&_svg]:size-4 pointer-coarse:min-h-11",
        lg: "h-10 px-4 text-sm [&_svg]:size-4",
        icon: "size-9 p-0 [&_svg]:size-4 pointer-coarse:size-11",
        "icon-sm": "size-8 p-0 [&_svg]:size-4 pointer-coarse:size-11",
        "icon-xs": "size-7 p-0 [&_svg]:size-3.5 pointer-coarse:size-11",
      },
    },
    defaultVariants: { variant: "primary", size: "md" },
  },
);

export type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof buttonVariants> & {
    /** Shows a spinner, keeps the label (so the width does not jump) and blocks a second submit. */
    loading?: boolean;
  };

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant, size, loading = false, className, type = "button", disabled, children, ...props },
  ref,
) {
  // type defaults to "button": a native button would otherwise submit the surrounding form.
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={cn(buttonVariants({ variant, size }), className)}
      {...props}
    >
      {loading && <Loader2 className="animate-spin motion-reduce:animate-none" aria-hidden />}
      {children}
    </button>
  );
});

/** Navigation styled as a button keeps link semantics, including open-in-new-tab. */
export function LinkButton({
  href,
  variant,
  size,
  className,
  children,
  ...props
}: Omit<React.ComponentProps<typeof Link>, "href"> & { href: string } & VariantProps<typeof buttonVariants>) {
  return (
    <Link href={href} className={cn(buttonVariants({ variant, size }), className)} {...props}>
      {children}
    </Link>
  );
}

export { buttonVariants };
