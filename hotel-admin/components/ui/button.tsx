"use client";
import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

// The variant set is unchanged in NAME — `primary | secondary | ghost | danger` — because four dozen call
// sites pass those strings. What changed is everything they resolve to: role tokens instead of hard-coded
// panel hexes, a real focus ring, and a disabled state that reads as unavailable rather than merely faded.
//
// `outline`, `subtle` and `link` are additions for the new screens. Nothing existing has to adopt them.
const buttonVariants = cva(
  cn(
    "inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-md font-medium",
    "transition-[background-color,border-color,color,box-shadow] duration-150",
    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    "disabled:pointer-events-none disabled:opacity-50",
    // Icons sent in as children should never be squashed by a flex parent.
    "[&_svg]:pointer-events-none [&_svg]:shrink-0",
  ),
  {
    variants: {
      variant: {
        primary: "bg-primary text-primary-foreground shadow-xs hover:bg-primary-hover active:bg-primary-hover",
        secondary:
          "border border-border bg-card text-foreground shadow-xs hover:bg-surface hover:border-border-strong",
        outline: "border border-border-strong bg-transparent text-foreground hover:bg-surface",
        ghost: "bg-transparent text-muted-foreground hover:bg-surface hover:text-foreground",
        subtle: "bg-surface text-foreground hover:bg-accent",
        danger: "bg-destructive text-destructive-foreground shadow-xs hover:bg-destructive/90",
        link: "bg-transparent text-primary underline-offset-4 hover:underline px-0",
      },
      size: {
        xs: "h-7 px-2 text-xs [&_svg]:size-3.5",
        sm: "h-8 px-3 text-sm [&_svg]:size-4",
        md: "h-9 px-3.5 text-sm [&_svg]:size-4",
        lg: "h-10 px-4 text-sm [&_svg]:size-4",
        icon: "h-9 w-9 p-0 [&_svg]:size-4",
        "icon-sm": "h-8 w-8 p-0 [&_svg]:size-4",
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
  // type defaults to "button". A bare <button> inside a <form> submits it, and several screens rely on
  // Cancel NOT submitting — a default that was quietly wrong before.
  return <button ref={ref} type={type} className={cn(buttonVariants({ variant, size }), className)} {...props} />;
});

export { buttonVariants };
