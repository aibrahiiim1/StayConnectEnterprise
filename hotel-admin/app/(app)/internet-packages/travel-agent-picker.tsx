"use client";

// TRAVEL AGENTS ARE CHOSEN, NOT TYPED.
//
// A travel-agent rule matches the name the PMS sends on each guest record. Typed by hand, "Sunny Tour" for
// "Sunny Tours" silently matches nobody and the package is never offered. So the rule is built from a list of
// the travel agents the PMS has actually named on stays (with how many are in house under each), searchable,
// several at a time. A name the PMS has not sent yet can still be added -- an agent whose first guests arrive
// next week -- but it is shown as such.
//
// The rule's value stays what it always was: the chosen names, comma-separated. The server matches them
// case-insensitively and ignoring surrounding spaces.

import { useEffect, useId, useMemo, useRef, useState } from "react";
import { api, TravelAgent } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Check, ChevronDown, Plus, X } from "lucide-react";

const splitNames = (s: string): string[] => s.split(",").map((x) => x.trim()).filter(Boolean);
const same = (a: string, b: string) => a.trim().toLowerCase() === b.trim().toLowerCase();

export function TravelAgentPicker({
  value,
  onChange,
  label,
  testId,
}: {
  /** Comma-separated names, as the rule stores them. */
  value: string;
  onChange: (v: string) => void;
  label: string;
  testId?: string;
}) {
  const [agents, setAgents] = useState<TravelAgent[] | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const root = useRef<HTMLDivElement>(null);
  const listId = useId();

  useEffect(() => {
    let live = true;
    api.get<{ data: TravelAgent[] }>("/pms-stays/travel-agents")
      .then((r) => { if (live) setAgents(r?.data ?? []); })
      .catch(() => { if (live) { setAgents([]); setLoadFailed(true); } });
    return () => { live = false; };
  }, []);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);

  const selected = splitNames(value);
  const isSelected = (n: string) => selected.some((s) => same(s, n));

  const options = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (agents ?? []).filter((a) => !q || a.name.toLowerCase().includes(q));
  }, [agents, query]);

  const typed = query.trim();
  // A comma would split one agent into two names when the rule is saved, so such a name cannot be added.
  const canAddTyped =
    typed !== "" && !typed.includes(",") &&
    !(agents ?? []).some((a) => same(a.name, typed)) && !isSelected(typed);

  function toggle(name: string) {
    onChange((isSelected(name) ? selected.filter((s) => !same(s, name)) : [...selected, name]).join(", "));
  }
  function remove(name: string) {
    onChange(selected.filter((s) => !same(s, name)).join(", "));
  }
  function addTyped() {
    if (!canAddTyped) return;
    onChange([...selected, typed].join(", "));
    setQuery("");
  }

  const known = (n: string) => (agents ?? []).some((a) => same(a.name, n));
  const rowCount = options.length + (canAddTyped ? 1 : 0);

  function onKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") { e.preventDefault(); setOpen(true); setActive((i) => Math.min(i + 1, rowCount - 1)); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setActive((i) => Math.max(i - 1, 0)); }
    else if (e.key === "Enter") {
      e.preventDefault();
      if (active < options.length && options[active]) toggle(options[active].name);
      else addTyped();
    } else if (e.key === "Escape") setOpen(false);
    else if (e.key === "Backspace" && query === "" && selected.length) remove(selected[selected.length - 1]);
  }

  return (
    <div ref={root} className="relative min-w-0 flex-1" data-testid={testId}>
      <div className="flex min-h-9 flex-wrap items-center gap-1 rounded-md border border-input bg-background px-1.5 py-1">
        {selected.map((n) => (
          <Badge key={n.toLowerCase()} tone={known(n) || agents === null ? "info" : "warn"} className="gap-1">
            {n}
            <button type="button" aria-label={`Remove ${n}`} onClick={() => remove(n)} className="opacity-70 hover:opacity-100">
              <X className="size-3" />
            </button>
          </Badge>
        ))}
        <Input
          role="combobox"
          aria-label={label}
          aria-expanded={open}
          aria-controls={listId}
          aria-autocomplete="list"
          placeholder={selected.length ? "Add another…" : "Search travel agents…"}
          className="h-7 min-w-32 flex-1 border-0 px-1 shadow-none focus-visible:ring-0"
          value={query}
          onFocus={() => setOpen(true)}
          onChange={(e) => { setQuery(e.target.value); setOpen(true); setActive(0); }}
          onKeyDown={onKeyDown}
        />
        <button type="button" aria-label="Show travel agents" onClick={() => setOpen((o) => !o)} className="text-muted-foreground">
          <ChevronDown className="size-4" />
        </button>
      </div>

      {open && (
        <ul
          id={listId}
          role="listbox"
          aria-multiselectable="true"
          aria-label="Travel agents"
          className="absolute z-20 mt-1 max-h-64 w-full overflow-auto rounded-md border border-border bg-surface p-1 shadow-lg"
        >
          {agents === null && <li className="px-2 py-1.5 text-sm text-muted-foreground">Loading travel agents…</li>}
          {agents !== null && options.length === 0 && !canAddTyped && (
            <li className="px-2 py-1.5 text-sm text-muted-foreground">
              {loadFailed
                ? "The travel-agent list could not be loaded. Type a name to add it."
                : agents.length === 0
                  ? "The PMS has not named any travel agent on a stay yet. Type a name to add it."
                  : "No travel agent matches."}
            </li>
          )}
          {options.map((a, i) => {
            const on = isSelected(a.name);
            return (
              <li
                key={a.name}
                role="option"
                aria-selected={on}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => toggle(a.name)}
                onMouseEnter={() => setActive(i)}
                className={`flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm ${i === active ? "bg-muted" : ""}`}
              >
                <span className="flex size-4 items-center justify-center">{on && <Check className="size-4" />}</span>
                <span className="flex-1 truncate">{a.name}</span>
                <span className="text-2xs text-muted-foreground tabular">
                  {a.in_house > 0 ? `${a.in_house} in house` : `${a.stays} stay${a.stays === 1 ? "" : "s"}`}
                </span>
              </li>
            );
          })}
          {canAddTyped && (
            <li
              role="option"
              aria-selected={false}
              onMouseDown={(e) => e.preventDefault()}
              onClick={addTyped}
              onMouseEnter={() => setActive(options.length)}
              className={`flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm ${active === options.length ? "bg-muted" : ""}`}
            >
              <Plus className="size-4" />
              <span>Add &ldquo;{typed}&rdquo; <span className="text-muted-foreground">(not yet seen from the PMS)</span></span>
            </li>
          )}
        </ul>
      )}
    </div>
  );
}
