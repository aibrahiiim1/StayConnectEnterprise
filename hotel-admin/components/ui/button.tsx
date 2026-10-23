"use client";
import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  cn(
    "inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-md font-medium",
    "transition-[background-color,border-color,color,box-shadow] duration-press ease-onegate",
    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    "disabled:pointer-events-none disabled:opacity-50",
    "[&_svg]:pointer-events-none [&_svg]:shrink-0",
  ),
  {
    variants: {
      variant: {
        primary:
          "bg-primary text-primary-foreground shadow-control hover:bg-primary-hover hover:shadow-control-hover active:bg-primary-press active:shadow-control-press",
        secondary:
          "border border-foreground/85 bg-card text-foreground hover:bg-accent hover:shadow-control-hover active:shadow-control-press",
        outline: "border border-input bg-transparent text-foreground hover:bg-accent active:shadow-control-press",
        ghost: "bg-transparent text-muted-foreground hover:bg-accent hover:text-foreground",
        subtle: "bg-accent text-foreground hover:bg-border/70",
        danger:
          "bg-destructive text-destructive-foreground shadow-control hover:bg-destructive/90 hover:shadow-control-hover active:shadow-control-press",
        link: "bg-transparent text-primary underline-offset-4 hover:underline px-0",
      },
      size: {
        // Coarse pointers get a 44px touch target.
        xs: "h-7 px-2 text-xs [&_svg]:size-3.5 pointer-coarse:min-h-11",
        sm: "h-8 px-3 text-label [&_svg]:size-4 pointer-coarse:min-h-11",
        md: "h-9 px-3.5 text-label [&_svg]:size-4 pointer-coarse:min-h-11",
        lg: "h-11 px-5 text-sm font-semibold [&_svg]:size-4",
        icon: "h-9 w-9 p-0 [&_svg]:size-4 pointer-coarse:size-11",
        "icon-sm": "h-8 w-8 p-0 [&_svg]:size-4 pointer-coarse:size-11",
      },
    },
    defaultVariants: { variant: "primary", size: "md" },
  },
);

export type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof buttonVariants>;

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant, size, className, type = "button", ...props },
  ref,
) {
  // Native buttons submit forms by default; cancellation must not submit.
  return <button ref={ref} type={type} className={cn(buttonVariants({ variant, size }), className)} {...props} />;
});

export { buttonVariants };
