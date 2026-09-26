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
    "transition-[background-color,border-color,color,box-shadow] duration-press ease-onegate",
    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    "disabled:pointer-events-none disabled:opacity-50",
    // Icons sent in as children should never be squashed by a flex parent.
    "[&_svg]:pointer-events-none [&_svg]:shrink-0",
  ),
  {
    variants: {
      variant: {
        // THE ONEGATE BUTTON FAMILY. primary: the brand fill, the ONE main action a screen exists for.
        // secondary: surface with an ink border, every other real action. quiet (ghost): transparent with a
        // slate label, for dismiss/cancel/tertiary. Hover lifts with a short shallow shadow; press sinks
        // with an inset one over 80ms. No bounce, no spring.
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
        xs: "h-7 px-2 text-xs [&_svg]:size-3.5",
        sm: "h-8 px-3 text-[0.8125rem] font-semibold [&_svg]:size-4",
        md: "h-9 px-3.5 text-[0.8125rem] font-semibold [&_svg]:size-4",
        lg: "h-11 px-5 text-sm font-semibold [&_svg]:size-4",
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
