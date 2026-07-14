"use client";
import * as React from "react";
import { cn } from "@/lib/utils";

type Variant = "primary" | "secondary" | "ghost" | "danger";
type Size = "sm" | "md";

export function Button({
  variant = "primary",
  size = "md",
  className,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: Size }) {
  const base =
    "inline-flex items-center justify-center gap-2 rounded-md font-medium transition-colors disabled:opacity-50 disabled:pointer-events-none";
  const sizes: Record<Size, string> = {
    sm: "h-8 px-3 text-sm",
    md: "h-9 px-4 text-sm",
  };
  const variants: Record<Variant, string> = {
    primary:   "bg-brand text-white hover:bg-brandDim",
    secondary: "bg-panel2 text-text border border-border hover:bg-[#222735]",
    ghost:     "bg-transparent text-muted hover:bg-panel2 hover:text-text",
    danger:    "bg-err text-white hover:opacity-90",
  };
  return <button className={cn(base, sizes[size], variants[variant], className)} {...props} />;
}
