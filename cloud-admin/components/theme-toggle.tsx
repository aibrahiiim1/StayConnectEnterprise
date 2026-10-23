"use client";

import * as React from "react";
import { useTheme } from "next-themes";
import { Monitor, Moon, Sun } from "lucide-react";
import { cn } from "@/lib/utils";

const OPTIONS = [
  { value: "light", label: "Light", Icon: Sun },
  { value: "dark", label: "Dark", Icon: Moon },
  { value: "system", label: "System", Icon: Monitor },
] as const;

/** Wait for mount because the server cannot know the theme; keep one tab stop for the radio group. */
export function ThemeToggle({ className }: { className?: string }) {
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = React.useState(false);
  React.useEffect(() => setMounted(true), []);
  const refs = React.useRef<(HTMLButtonElement | null)[]>([]);

  const checkedIndex = mounted ? OPTIONS.findIndex((o) => o.value === theme) : -1;
  const tabStop = checkedIndex >= 0 ? checkedIndex : 0;

  function choose(i: number) {
    const next = (i + OPTIONS.length) % OPTIONS.length;
    setTheme(OPTIONS[next].value);
    refs.current[next]?.focus();
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLButtonElement>, i: number) {
    // Arrow direction follows visual order in right-to-left layouts.
    const rtl = getComputedStyle(e.currentTarget).direction === "rtl";
    const forward = rtl ? "ArrowLeft" : "ArrowRight";
    const back = rtl ? "ArrowRight" : "ArrowLeft";
    let target: number | null = null;
    if (e.key === forward || e.key === "ArrowDown") target = i + 1;
    else if (e.key === back || e.key === "ArrowUp") target = i - 1;
    else if (e.key === "Home") target = 0;
    else if (e.key === "End") target = OPTIONS.length - 1;
    if (target === null) return;
    e.preventDefault();
    choose(target);
  }

  return (
    <div
      role="radiogroup"
      aria-label="Colour theme"
      className={cn(
        "inline-flex items-center gap-0.5 rounded-md border border-border bg-surface p-0.5",
        className,
      )}
    >
      {OPTIONS.map(({ value, label, Icon }, i) => {
        const active = i === checkedIndex;
        return (
          <button
            key={value}
            ref={(el) => { refs.current[i] = el; }}
            type="button"
            role="radio"
            aria-checked={active}
            tabIndex={i === tabStop ? 0 : -1}
            title={label}
            onClick={() => setTheme(value)}
            onKeyDown={(e) => onKeyDown(e, i)}
            className={cn(
              "inline-flex size-6 items-center justify-center rounded-sm transition-colors pointer-coarse:size-11",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
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
