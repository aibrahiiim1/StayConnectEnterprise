import * as React from "react";
import { cn } from "@/lib/utils";

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className, ...p }, ref) {
    return (
      <input
        ref={ref}
        className={cn(
          "h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm",
          "placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-brand/40 focus:border-brand",
          className
        )}
        {...p}
      />
    );
  }
);

export function Label({ className, ...p }: React.LabelHTMLAttributes<HTMLLabelElement>) {
  return <label className={cn("block text-xs text-muted mb-1", className)} {...p} />;
}
