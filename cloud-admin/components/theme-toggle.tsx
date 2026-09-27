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

/**
 * ThemeToggle — the control the operator asked for: dark is an option, not the only mode.
 *
 * Three explicit states rather than a two-way switch. "System" matters on an appliance that is left open on a
 * front-desk machine all day: the screen should follow the desk's own light/dark schedule without anyone
 * touching it.
 *
 * `mounted` guards the render because the resolved theme is not knowable on the server. Showing the wrong
 * segment as selected for one frame is worse than showing a neutral strip for one frame.
 *
 * It is a radio group, so it behaves like one (WAI-ARIA APG): ONE tab stop, on the checked option, and the
 * arrow keys, Home and End move the choice. Three tab stops that ignore the arrows only looked like radios.
 */
export function ThemeToggle({ className }: { className?: string }) {
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = React.useState(false);
  React.useEffect(() => setMounted(true), []);
  const refs = React.useRef<(HTMLButtonElement | null)[]>([]);

  const checkedIndex = mounted ? OPTIONS.findIndex((o) => o.value === theme) : -1;
  // Before mount (or for an unknown value) nothing is checked, so the first option carries the tab stop.
  const tabStop = checkedIndex >= 0 ? checkedIndex : 0;

  function choose(i: number) {
    const next = (i + OPTIONS.length) % OPTIONS.length;
    setTheme(OPTIONS[next].value);
    refs.current[next]?.focus();
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLButtonElement>, i: number) {
    // In right-to-left text the visual order flips, so Left/Right follow what the operator sees.
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
