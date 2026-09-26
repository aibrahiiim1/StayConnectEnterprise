"use client";

// ADVANCED HTML & CSS — the escape hatch, with the server's own verdict beside it.
//
// This is the page guests type their room number, surname and voucher codes into. Styling and markup are
// welcome; anything that could run, load from elsewhere or re-target the sign-in forms is not. The editor does
// not guess at those rules: it sends the text to /portal-branding/validate and shows what the portal would
// remove -- "removed the onerror attribute on <img>" -- while the operator is still looking at it, and offers
// the cleaned version with one click. Saving a change here (including emptying a field) asks for the
// operator's password.

import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Callout } from "@/components/ui/error-banner";
import { CodeEditor } from "@/components/ui/code-editor";
import { CheckCircle2, ShieldCheck, Wand2 } from "lucide-react";
import { LIMITS, type DesignIssue } from "@/lib/api/portal-design";
import { Design } from "./strings";

const CSS_EXAMPLE = `/* Your rules take precedence over the portal's own styling. */
.card { box-shadow: 0 20px 60px rgba(0, 0, 0, .25); }
.brand .name { letter-spacing: .04em; text-transform: uppercase; }`;

const HTML_EXAMPLE = `<section class="amenities">
  <h3>Around the resort</h3>
  <ul><li>Pool 08:00–20:00</li><li>Spa 10:00–22:00</li></ul>
</section>`;

export function AdvancedSection({ d, set, writable, issues, sanitized, checking, needsPassword }: {
  d: Design;
  set: <K extends keyof Design>(k: K, v: Design[K]) => void;
  writable: boolean;
  issues: DesignIssue[];
  sanitized: { custom_css: string; custom_html: string } | null;
  checking: boolean;
  needsPassword: boolean;
}) {
  return (
    <div className="space-y-5">
      <Callout tone="warning" title="This page collects guests' room numbers and voucher codes">
        Styling and markup are accepted. <strong>Scripts, event handlers, frames, forms, external stylesheets,
        &lt;base&gt;, &lt;meta&gt; and @import are not</strong> — anything that could run or send a guest&apos;s
        details elsewhere. The portal applies the same rules again before any guest receives the page, and the
        sign-in controls stay visible and usable whatever the stylesheet says. Saving a change here, including
        clearing a field, asks for your password.
      </Callout>
      {needsPassword && (
        <Callout tone="info" title="Your password will be needed to save">
          You changed the custom CSS or HTML. Save changes will ask you to confirm your password.
        </Callout>
      )}

      <CodeCard
        field="custom_css"
        title="Custom CSS"
        description="Your rules beat the portal's styling, but cannot hide the sign-in forms."
        language="css"
        value={d.custom_css ?? ""}
        onChange={(v) => set("custom_css", v)}
        writable={writable}
        issues={issues.filter((i) => i.field === "custom_css")}
        cleaned={sanitized?.custom_css ?? null}
        checking={checking}
        example={CSS_EXAMPLE}
      />
      <CodeCard
        field="custom_html"
        title="Custom HTML"
        description="Shown below the sign-in."
        language="html"
        value={d.custom_html ?? ""}
        onChange={(v) => set("custom_html", v)}
        writable={writable}
        issues={issues.filter((i) => i.field === "custom_html")}
        cleaned={sanitized?.custom_html ?? null}
        checking={checking}
        example={HTML_EXAMPLE}
      />
    </div>
  );
}

function CodeCard({
  field, title, description, language, value, onChange, writable, issues, cleaned, checking, example,
}: {
  field: "custom_css" | "custom_html";
  title: string;
  description: string;
  language: "css" | "html";
  value: string;
  onChange: (v: string) => void;
  writable: boolean;
  issues: DesignIssue[];
  cleaned: string | null;
  checking: boolean;
  example: string;
}) {
  const errors = issues.filter((i) => i.severity === "error");
  const warnings = issues.filter((i) => i.severity === "warning");
  const listId = `${field}-findings`;
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0 space-y-1">
            <CardTitle>{title}</CardTitle>
            <CardDescription>{description}</CardDescription>
          </div>
          {value.trim() === ""
            ? <Badge tone="neutral">Empty</Badge>
            : checking
              ? <Badge tone="neutral">Checking…</Badge>
              : errors.length
                ? <Badge tone="err" dot>{errors.length} to fix</Badge>
                : <Badge tone="ok" dot>Accepted</Badge>}
        </div>
      </CardHeader>
      <CardBody className="space-y-3">
        <CodeEditor
          id={field}
          label={title}
          language={language}
          value={value}
          onChange={onChange}
          readOnly={!writable}
          maxBytes={LIMITS.advancedBytes}
          minRows={12}
          placeholder={example}
        />
        {value.trim() !== "" && !checking && issues.length === 0 && (
          <p className="flex items-center gap-1.5 text-xs text-success">
            <CheckCircle2 className="h-3.5 w-3.5" aria-hidden /> The portal will serve this exactly as written.
          </p>
        )}
        {issues.length > 0 && (
          <div className="space-y-2 rounded-md border border-border bg-surface p-3" aria-live="polite">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="flex items-center gap-1.5 text-sm font-medium">
                <ShieldCheck className="h-4 w-4 text-muted-foreground" aria-hidden />
                {errors.length ? "The portal would remove:" : "The portal will adjust:"}
              </p>
              {errors.length > 0 && cleaned !== null && writable && (
                <Button size="sm" variant="secondary" onClick={() => onChange(cleaned)}>
                  <Wand2 className="mr-1.5 h-3.5 w-3.5" aria-hidden /> Use the cleaned version
                </Button>
              )}
            </div>
            <ul id={listId} className="space-y-1 text-sm">
              {[...errors, ...warnings].map((i, n) => (
                <li key={n} className="flex items-start gap-2">
                  <Badge tone={i.severity === "error" ? "err" : "warn"}>{i.severity === "error" ? "Removed" : "Note"}</Badge>
                  <span className="min-w-0 break-words">{i.message}</span>
                </li>
              ))}
            </ul>
            {errors.length > 0 && (
              <p className="text-xs text-muted-foreground">
                The preview shows the cleaned version, which is what a guest would receive. Save is refused until
                these are fixed, so what is stored is exactly what guests see.
              </p>
            )}
          </div>
        )}
      </CardBody>
    </Card>
  );
}
