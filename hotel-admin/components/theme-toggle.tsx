"use client";

import * as React from "react";
import { useTheme } from "next-themes";
import { Monitor, Moon, Sun } from "lucide-react";
import { cn } from "@/lib/utils";
import { useRadioKeys } from "@/components/ui/data";

const OPTIONS = [
  { value: "light", label: "Light", Icon: Sun },
  { value: "dark", label: "Dark", Icon: Moon },
  { value: "system", label: "System", Icon: Monitor },
] as const;
const VALUES = OPTIONS.map((o) => o.value);

/** Wait for mount because the server cannot know which theme the operator has selected. */
export function ThemeToggle({ className }: { className?: string }) {
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = React.useState(false);
  React.useEffect(() => setMounted(true), []);
  const current = mounted ? theme ?? "system" : "system";
  const onKeyDown = useRadioKeys<string>(VALUES, current, setTheme);

  return (
    // The checked radio is the group's sole tab stop under roving tabindex.
    // eslint-disable-next-line jsx-a11y/interactive-supports-focus
    <div
      role="radiogroup"
      aria-label="Colour theme"
      onKeyDown={onKeyDown}
      className={cn(
        "inline-flex items-center gap-0.5 rounded-md border border-border bg-surface p-0.5",
        className,
      )}
    >
      {OPTIONS.map(({ value, label, Icon }) => {
        const active = mounted && theme === value;
        return (
          <button
            key={value}
            type="button"
            role="radio"
            aria-checked={active}
            tabIndex={value === current ? 0 : -1}
            title={label}
            onClick={() => setTheme(value)}
            className={cn(
              "inline-flex size-6 items-center justify-center rounded-[5px] transition-colors pointer-coarse:size-11",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
              active
                ? "bg-card text-foreground shadow-xs"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            <Icon className="size-3.5" />
            <span className="sr-only">{label}</span>
          </button>
        );
      })}
    </div>
  );
}
