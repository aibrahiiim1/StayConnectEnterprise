"use client";

// LANGUAGES — what a guest is offered, and what the portal says in each one.
//
// THE DEFECT THIS REPLACES. Picking Italiano did nothing an operator could see: the editor had only the
// ENGLISH list, so it drew fifty empty boxes with the English as placeholder text. The portal has spoken
// complete Italian the whole time; the screen simply had no way to show it, and "select a language, nothing
// changes" is indistinguishable from "this feature is broken".
//
// The fix is not to copy six dictionaries into the admin. That is 306 strings maintained twice, and drift
// would surface as an operator confidently editing wording nobody reads. The portal owns its words; this
// fetches them through edged and shows them.
//
// WHAT AN OPERATOR SEES NOW. Every field is filled in with the wording a guest actually gets. Typing over it
// makes a customisation; putting it back removes the customisation rather than storing a second copy of the
// shipped text. A hotel that has changed nothing stores nothing, and its Languages screen still shows it
// exactly what its guests read.

import { useEffect, useMemo, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Callout } from "@/components/ui/error-banner";
import { SearchInput } from "@/components/ui/data";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { RotateCcw, Check } from "lucide-react";
import {
  Design, PORTAL_STRINGS, STRING_GROUPS, SHIPPED_LANGUAGES, offeredLanguages,
} from "./strings";

/** What the portal ships, as it reports it: code -> key -> wording. */
export type ShippedWording = {
  languages: { code: string; label: string; rtl?: boolean }[];
  strings: Record<string, Record<string, string>>;
};

export function LanguagesSection({ d, setD, writable, part = "all", onEditWording, initialEditing }: {
  d: Design;
  setD: (f: (p: Design) => Design) => void;
  writable: boolean;
  /** The designer shows "which languages guests are offered" and "what the page says" as two sections. */
  part?: "all" | "offered" | "wording";
  /** Where "Add another language" sends the operator to type the new language's wording. */
  onEditWording?: (code: string) => void;
  /** Which language the wording editor opens on. */
  initialEditing?: string;
}) {
  const [shipped, setShipped] = useState<ShippedWording | null>(null);
  const [shippedErr, setShippedErr] = useState<string | null>(null);
  const [editing, setEditing] = useState(initialEditing ?? "en");
  const [filter, setFilter] = useState("");
  const [newCode, setNewCode] = useState("");
  const [newLabel, setNewLabel] = useState("");

  useEffect(() => {
    let live = true;
    api.get<ShippedWording>("/portal-branding/languages")
      .then((r) => { if (live) { setShipped(r); setShippedErr(null); } })
      .catch((e) => {
        if (live) setShippedErr(e instanceof ApiError ? e.message : "the built-in wording could not be read");
      });
    return () => { live = false; };
  }, []);

  /** Every language this appliance knows about: what the portal ships, plus anything the hotel added. */
  const known = useMemo(() => {
    const seen = new Map<string, { code: string; label: string; rtl?: boolean }>();
    (shipped?.languages ?? SHIPPED_LANGUAGES).forEach((l) => seen.set(l.code, l));
    (d.languages ?? []).forEach((l) => { if (!seen.has(l.code)) seen.set(l.code, l); });
    Object.keys(d.translations ?? {}).forEach((c) => {
      if (!seen.has(c)) seen.set(c, { code: c, label: c.toUpperCase() });
    });
    return Array.from(seen.values());
  }, [shipped, d.languages, d.translations]);

  const offered = useMemo(() => offeredLanguages(d).map((l) => l.code), [d]);
  const current = known.find((l) => l.code === editing) ?? known[0];
  const builtinFor = (code: string) => shipped?.strings?.[code] ?? {};
  /** WHETHER THE PORTAL SHIPS THIS LANGUAGE, which is not the same question as whether its wording has
   *  arrived yet. Asking only the fetched map meant that for the second it was in flight — and for as long as
   *  the portal service was down — every shipped language was reported as "51 strings will show English",
   *  which is both alarming and false. The six are known to ship; only their words are being fetched. */
  const isBuiltIn = (code: string) =>
    Object.keys(builtinFor(code)).length > 0 || SHIPPED_LANGUAGES.some((l) => l.code === code);

  /** The wording a guest receives for one string: the hotel's, or the portal's, or English. */
  function effective(code: string, key: string) {
    const own = d.translations?.[code]?.[key];
    if (own) return own;
    return builtinFor(code)[key] ?? "";
  }
  const customised = (code: string, key: string) => !!d.translations?.[code]?.[key];

  function countCustom(code: string) {
    const tr = d.translations?.[code] ?? {};
    return PORTAL_STRINGS.filter((s) => (tr[s.key] ?? "").trim() !== "").length;
  }
  /** Only a language the portal does not ship can be short of words. */
  function countMissing(code: string) {
    if (isBuiltIn(code)) return 0;
    return PORTAL_STRINGS.length - countCustom(code);
  }

  function setOffered(code: string, on: boolean) {
    setD((p) => {
      const list = offeredLanguages(p);
      const label = known.find((l) => l.code === code)?.label ?? code.toUpperCase();
      const next = on
        ? [...list.filter((l) => l.code !== code), { code, label }]
        : list.filter((l) => l.code !== code || code === "en");
      const order = known.map((l) => l.code);
      next.sort((a, b) => order.indexOf(a.code) - order.indexOf(b.code));
      return { ...p, languages: next };
    });
  }

  /** AN OVERRIDE IS ONLY STORED WHEN IT IS ACTUALLY DIFFERENT.
   *
   *  The field is pre-filled with the shipped wording, so without this every language an operator merely
   *  LOOKED at would be saved back as fifty-one "customisations" identical to the built-ins — a document that
   *  grows without bound and freezes the hotel's copy of wording the product is supposed to be able to
   *  improve under it. Typing the shipped text back in, or pressing Reset, removes the key. */
  function write(code: string, key: string, value: string) {
    setD((p) => {
      const all = { ...(p.translations ?? {}) };
      const one = { ...(all[code] ?? {}) };
      const base = builtinFor(code)[key] ?? "";
      if (!value.trim() || value === base) delete one[key];
      else one[key] = value;
      if (Object.keys(one).length) all[code] = one;
      else delete all[code];
      return { ...p, translations: all };
    });
  }

  function resetLanguage(code: string) {
    setD((p) => {
      const all = { ...(p.translations ?? {}) };
      delete all[code];
      return { ...p, translations: all };
    });
  }

  const needle = filter.trim().toLowerCase();
  const visible = (key: string, english: string) =>
    !needle || key.includes(needle) || english.toLowerCase().includes(needle) ||
    effective(current?.code ?? "en", key).toLowerCase().includes(needle);

  const showOffered = part !== "wording";
  const showWording = part !== "offered";

  // Adding a language is a change, so a role that cannot save is not offered it at all.
  const addLanguage = !writable ? null : (
    <div className="space-y-2 border-t border-border pt-4">
      <div>
        <div className="text-label">Add another language</div>
        <p className="text-xs text-muted-foreground">
          One the portal does not ship. You supply its wording; anything you leave empty shows English.
        </p>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <div>
          <Label htmlFor="add-language-code" className="text-xs">Language code</Label>
          <Input id="add-language-code" value={newCode} className="w-24" placeholder="es"
            onChange={(e) => setNewCode(e.target.value.toLowerCase().slice(0, 5))} />
        </div>
        <div>
          <Label htmlFor="add-language-label" className="text-xs">Shown as</Label>
          <Input id="add-language-label" value={newLabel} className="w-44" placeholder="Español"
            onChange={(e) => setNewLabel(e.target.value)} />
        </div>
        <Button variant="secondary" className="h-10" disabled={!newCode.trim()}
          onClick={() => {
            const code = newCode.trim();
            setD((p) => ({
              ...p,
              languages: [
                ...offeredLanguages(p).filter((l) => l.code !== code),
                { code, label: newLabel.trim() || code.toUpperCase() },
              ],
            }));
            setEditing(code); setNewCode(""); setNewLabel("");
            onEditWording?.(code);
          }}>
          Add
        </Button>
      </div>
    </div>
  );

  return (
    <div className="space-y-5">
      {/* ---- which languages guests are offered ----------------------------------------------------- */}
      {showOffered && (
      <Card>
        <CardHeader><CardTitle>Guest languages</CardTitle></CardHeader>
        <CardBody className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Choose which of the {SHIPPED_LANGUAGES.length} built-in languages guests are offered. English is always
            available.
          </p>
          <div className="flex flex-wrap gap-2">
            {known.map((l) => {
              const on = offered.includes(l.code);
              const fixed = l.code === "en";
              return (
                <label key={l.code}
                  className={cn(
                    "inline-flex min-h-10 cursor-pointer items-center gap-2 rounded-full border px-3.5 py-2 text-sm transition-colors",
                    "has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring has-[:focus-visible]:ring-offset-2",
                    on ? "border-primary/40 bg-primary-subtle text-primary-subtle-foreground" : "border-border text-muted-foreground hover:border-border-strong",
                    (fixed || !writable) && "cursor-default",
                  )}>
                  <input type="checkbox" className="size-4 accent-primary" checked={on}
                    disabled={fixed || !writable}
                    aria-label={`Offer ${l.label} to guests`}
                    onChange={(e) => setOffered(l.code, e.target.checked)} />
                  {l.label}
                  {!isBuiltIn(l.code) && <Badge tone="warn">needs wording</Badge>}
                </label>
              );
            })}
          </div>
          {part === "offered" && addLanguage}
        </CardBody>
      </Card>
      )}

      {/* ---- the wording itself --------------------------------------------------------------------- */}
      {showWording && (
      <Card>
        <CardHeader><CardTitle>Wording</CardTitle></CardHeader>
        <CardBody className="space-y-4">
          {shippedErr && (
            <Callout tone="warning">{shippedErr}</Callout>
          )}

          <div className="flex flex-wrap items-center gap-2" role="tablist" aria-label="Language being edited"
            onKeyDown={(e) => {
              const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
              if (!step || known.length === 0) return;
              e.preventDefault();
              const i = Math.max(0, known.findIndex((l) => l.code === current?.code));
              const next = known[(i + step + known.length) % known.length];
              setEditing(next.code);
              document.getElementById(`wording-tab-${next.code}`)?.focus();
            }}>
            {known.map((l) => {
              const custom = countCustom(l.code);
              const missing = countMissing(l.code);
              return (
                <button key={l.code} id={`wording-tab-${l.code}`} type="button" role="tab"
                  aria-selected={current?.code === l.code}
                  tabIndex={current?.code === l.code ? 0 : -1}
                  onClick={() => setEditing(l.code)}
                  className={cn(
                    "inline-flex items-center gap-2 rounded-md border px-3 py-2 text-sm transition-colors",
                    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
                    current?.code === l.code
                      ? "border-primary/40 bg-primary-subtle font-medium text-primary-subtle-foreground"
                      : "border-border text-muted-foreground hover:border-border-strong hover:text-foreground",
                  )}>
                  {l.label}
                  {missing > 0
                    ? <Badge tone="warn">{missing} in English</Badge>
                    : custom > 0
                      ? <Badge tone="accent">{custom} customised</Badge>
                      : <Badge tone="default">Built-in</Badge>}
                </button>
              );
            })}
          </div>

          {current && (
            <div className="space-y-3 rounded-lg border border-border p-4">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div>
                  <strong className="text-sm">{current.label}</strong>
                  <p className="text-xs text-muted-foreground">
                    {isBuiltIn(current.code)
                      ? countCustom(current.code) === 0
                        ? "Everything below is the wording that ships with the portal. This is exactly what your guests read."
                        : `${countCustom(current.code)} of ${PORTAL_STRINGS.length} strings have been changed by this hotel. The rest are the wording that ships with the portal.`
                      : "This hotel added this language, so the portal has no wording for it. Anything left empty shows English."}
                  </p>
                </div>
                <span className="flex items-center gap-2">
                  <SearchInput value={filter} onChange={setFilter} label="Find a string"
                    placeholder="Find a string…" className="sm:w-56" />
                  {countCustom(current.code) > 0 && writable && (
                    <Button size="sm" variant="secondary" onClick={() => resetLanguage(current.code)}>
                      <RotateCcw aria-hidden />
                      Reset all to built-in
                    </Button>
                  )}
                </span>
              </div>

              {!shipped && !shippedErr && (
                <p className="text-sm text-muted-foreground">Reading the portal&apos;s built-in wording…</p>
              )}

              {STRING_GROUPS.map((g) => {
                const rows = PORTAL_STRINGS.filter((s) => s.group === g && visible(s.key, s.english));
                if (!rows.length) return null;
                const changed = rows.filter((s) => customised(current.code, s.key)).length;
                return (
                  <details key={g} open={!!needle || !isBuiltIn(current.code) || g === STRING_GROUPS[0]}
                    className="rounded-md border border-border">
                    <summary className="flex cursor-pointer items-center gap-2 rounded-md px-3 py-2 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                      {g}
                      <span className="text-xs font-normal text-muted-foreground">{rows.length}</span>
                      {changed > 0 && <Badge tone="accent">{changed} customised</Badge>}
                    </summary>
                    <div className="grid gap-3 p-3 pt-0 sm:grid-cols-2">
                      {rows.map((st) => {
                        const own = customised(current.code, st.key);
                        const value = effective(current.code, st.key);
                        return (
                          <div key={st.key}>
                            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                              {st.english}
                              {own
                                ? <Badge tone="accent">Customised</Badge>
                                : isBuiltIn(current.code)
                                  ? null
                                  : <Badge tone="warn">English</Badge>}
                            </span>
                            <span className="mt-1 flex items-center gap-1.5">
                              <Input
                                value={value}
                                placeholder={st.english}
                                disabled={!writable}
                                dir={current.rtl ? "rtl" : undefined}
                                aria-label={`${st.english} in ${current.label}`}
                                onChange={(e) => write(current.code, st.key, e.target.value)}
                              />
                              {own && writable && (
                                <Button size="sm" variant="secondary"
                                  aria-label={`Reset ${st.english} in ${current.label} to the built-in wording`}
                                  onClick={() => write(current.code, st.key, "")}>
                                  <RotateCcw aria-hidden />
                                </Button>
                              )}
                            </span>
                          </div>
                        );
                      })}
                    </div>
                  </details>
                );
              })}
            </div>
          )}

          {/* A language the portal does not ship. It arrives empty, and says so. */}
          {part === "all" && addLanguage}

          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <Check className="size-3.5 shrink-0" aria-hidden />
            Only the strings you actually change are stored. Everything else follows the portal, so improved
            wording reaches your guests without you re-entering anything.
          </p>
        </CardBody>
      </Card>
      )}
    </div>
  );
}
