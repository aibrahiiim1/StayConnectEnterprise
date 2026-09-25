"use client";

// CODE EDITOR — for the portal designer's custom CSS and HTML.
//
// Deliberately NOT a bundled editor (CodeMirror/Monaco are hundreds of kilobytes, shipped to an appliance, for
// two fields an administrator edits a few times a year). What an operator actually needs from a code field is:
// a monospace face, line numbers to match an error message against, Tab that indents instead of leaving the
// field, no spellcheck or autocorrect mangling selectors, and a visible size budget. That is this.

import * as React from "react";
import { cn } from "@/lib/utils";

export const CodeEditor = React.forwardRef<
  HTMLTextAreaElement,
  {
    value: string;
    onChange: (v: string) => void;
    language: "css" | "html";
    label: string;
    id?: string;
    /** Maximum length in bytes, shown as a budget. */
    maxBytes?: number;
    minRows?: number;
    placeholder?: string;
    readOnly?: boolean;
    className?: string;
  }
>(function CodeEditor(
  { value, onChange, language, label, id, maxBytes, minRows = 14, placeholder, readOnly, className },
  ref,
) {
  const gutter = React.useRef<HTMLDivElement>(null);
  const lines = Math.max(minRows, value.split("\n").length);
  const bytes = React.useMemo(() => new TextEncoder().encode(value).length, [value]);
  const over = maxBytes != null && bytes > maxBytes;

  // Escape "releases" the field: the NEXT Tab moves focus on instead of indenting. Without this a keyboard user
  // who tabs into the editor can never tab out of it, which is a keyboard trap (WCAG 2.1.2).
  const released = React.useRef(false);

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Escape") {
      released.current = true;
      return;
    }
    if (e.key !== "Tab" || e.shiftKey || e.altKey || e.ctrlKey || e.metaKey) {
      released.current = false;
      return;
    }
    if (released.current) {
      released.current = false;
      return; // let the browser move focus
    }
    e.preventDefault();
    const el = e.currentTarget;
    const start = el.selectionStart;
    const end = el.selectionEnd;
    const next = value.slice(0, start) + "  " + value.slice(end);
    onChange(next);
    requestAnimationFrame(() => {
      el.selectionStart = el.selectionEnd = start + 2;
    });
  };

  return (
    <div className={cn("space-y-1.5", className)}>
      <div
        className={cn(
          "flex overflow-hidden rounded-md border bg-background font-mono text-xs shadow-xs",
          over ? "border-destructive" : "border-input",
          "focus-within:ring-2 focus-within:ring-ring/50",
        )}
      >
        <div
          ref={gutter}
          aria-hidden
          className="select-none overflow-hidden border-r border-border bg-surface/60 px-2 py-2 text-right leading-5 text-muted-foreground/60"
          style={{ height: `${minRows * 1.25 + 1}rem` }}
        >
          {Array.from({ length: lines }, (_, i) => (
            <div key={i} className="tabular">
              {i + 1}
            </div>
          ))}
        </div>
        <textarea
          ref={ref}
          id={id}
          aria-label={label}
          value={value}
          readOnly={readOnly}
          placeholder={placeholder}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          autoComplete="off"
          wrap="off"
          onKeyDown={onKeyDown}
          onChange={(e) => onChange(e.target.value)}
          onScroll={(e) => {
            if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop;
          }}
          data-language={language}
          className="min-w-0 flex-1 resize-none bg-transparent px-3 py-2 leading-5 outline-none placeholder:text-muted-foreground/60"
          style={{ height: `${minRows * 1.25 + 1}rem`, tabSize: 2 }}
        />
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2 text-2xs text-muted-foreground">
        <span>Tab indents. Press Esc, then Tab, to move to the next field.</span>
        {maxBytes != null && (
          <span className={cn("tabular", over && "font-medium text-destructive")}>
            {(bytes / 1024).toFixed(1)} KB of {(maxBytes / 1024).toFixed(0)} KB
          </span>
        )}
      </div>
    </div>
  );
});
