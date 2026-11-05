"use client";

// A travel-agent rule matches the exact name the PMS sends, so agents are picked from those the PMS has named
// (a typo would silently match nobody). Unseen names can still be added and are flagged. The value is a list
// and a name is never split on commas ("Sun Tours, Ltd" is one agent); the server matches case-insensitively.

import { useEffect, useId, useMemo, useRef, useState } from "react";
import { api, TravelAgent } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Check, ChevronDown, Plus, X } from "lucide-react";

const same = (a: string, b: string) => a.trim().toLowerCase() === b.trim().toLowerCase();

export function TravelAgentPicker({
  value,
  onChange,
  label,
  testId,
}: {
  value: string[];
  onChange: (v: string[]) => void;
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
      if (root.current && !root.current.contains(e.target as Node)) { setOpen(false); setActive(0); }
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);

  const selected = value;
  const isSelected = (n: string) => selected.some((s) => same(s, n));

  const options = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (agents ?? []).filter((a) => !q || a.name.toLowerCase().includes(q));
  }, [agents, query]);

  const typed = query.trim();
  const canAddTyped =
    typed !== "" && !(agents ?? []).some((a) => same(a.name, typed)) && !isSelected(typed);

  function toggle(name: string) {
    onChange(isSelected(name) ? selected.filter((s) => !same(s, name)) : [...selected, name]);
  }
  function remove(name: string) {
    onChange(selected.filter((s) => !same(s, name)));
  }
  function addTyped() {
    if (!canAddTyped) return;
    onChange([...selected, typed]);
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
    } else if (e.key === "Escape") { setOpen(false); setActive(0); }
    else if (e.key === "Backspace" && query === "" && selected.length) remove(selected[selected.length - 1]);
  }

  return (
    <div ref={root} className="relative min-w-0 flex-1" data-testid={testId}>
      <div className="flex min-h-9 flex-wrap items-center gap-1 rounded-md border border-input bg-background px-1.5 py-1">
        {selected.map((n) => (
          <Badge key={n.toLowerCase()} tone={known(n) || agents === null ? "info" : "warn"} className="gap-1">
            {n}
            <button type="button" aria-label={`Remove ${n}`} onClick={() => remove(n)}
              className="-me-1 inline-flex size-5 items-center justify-center rounded opacity-70 hover:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring pointer-coarse:size-7">
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
          aria-activedescendant={open && rowCount > 0 ? `${listId}-opt-${Math.min(active, rowCount - 1)}` : undefined}
          placeholder={selected.length ? "Add another…" : "Search travel agents…"}
          className="h-7 min-w-32 flex-1 border-0 px-1 shadow-none focus-visible:ring-0"
          value={query}
          onFocus={() => { if (!open) setActive(0); setOpen(true); }}
          onChange={(e) => { setQuery(e.target.value); setOpen(true); setActive(0); }}
          onKeyDown={onKeyDown}
        />
        <button type="button" aria-label="Show travel agents" aria-expanded={open} onClick={() => { setActive(0); setOpen((o) => !o); }}
          className="inline-flex size-7 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring pointer-coarse:size-9">
          <ChevronDown className="size-4" />
        </button>
      </div>

      {open && (
        <ul
          id={listId}
          role="listbox"
          aria-multiselectable="true"
          aria-label="Travel agents"
          className="absolute z-20 mt-1 max-h-64 w-full origin-top overflow-auto rounded-md border border-border bg-surface p-1 shadow-lg animate-in fade-in-0 zoom-in-95 duration-150 ease-onegate motion-reduce:animate-none"
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
              // The keyboard belongs to the combobox input; options are never focused.
              // eslint-disable-next-line jsx-a11y/click-events-have-key-events
              <li
                key={a.name}
                id={`${listId}-opt-${i}`}
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
            // eslint-disable-next-line jsx-a11y/click-events-have-key-events
            <li
              id={`${listId}-opt-${options.length}`}
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
