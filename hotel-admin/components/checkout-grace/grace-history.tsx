"use client";

// The append-only publication record, as a timeline. Nothing here hides or deletes a version: older versions are
// only collapsed behind "Show more", and each one opens in a sheet with the exact terms it put in force.

import * as React from "react";
import { History } from "lucide-react";
import {
  type GraceHistoryItem,
  changedTerms,
  compareTerms,
  fmtData,
  fmtDuration,
  fmtSpeed,
  fmtWhen,
  guestReceivesSentence,
  reasonLabel,
} from "@/lib/api/checkout-grace";
import { Timeline, KeyValueGrid } from "@/components/ui/data";
import { Sheet, SheetBody, SheetContent, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatRelative } from "@/lib/utils";

const PREVIEW = 5;

function headline(p: GraceHistoryItem["policy"]): string {
  if (!p) return "Terms not recorded";
  const bits: string[] = [];
  if (p.grace_duration_seconds) bits.push(fmtDuration(p.grace_duration_seconds));
  if (p.grace_down_kbps) bits.push(`${fmtSpeed(p.grace_down_kbps)} down`);
  if (p.grace_data_quota_bytes) bits.push(fmtData(p.grace_data_quota_bytes));
  return bits.join(" · ") || "Terms not recorded";
}

export function GraceHistory({ history, inForce }: { history: GraceHistoryItem[]; inForce: number }) {
  const [showAll, setShowAll] = React.useState(false);
  const [open, setOpen] = React.useState<GraceHistoryItem | null>(null);
  const shown = showAll ? history : history.slice(0, PREVIEW);

  const items = shown.map((h) => {
    const idx = history.indexOf(h);
    const prev = history[idx + 1]; // newest first, so the previous version is the next entry
    const changes = prev ? changedTerms(prev.policy, h.policy) : [];
    return {
      key: String(h.config_version),
      tone: (h.config_version === inForce ? "ok" : "neutral") as "ok" | "neutral",
      title: (
        <span className="inline-flex flex-wrap items-center gap-2">
          Version {h.config_version}
          {h.config_version === inForce && (
            <Badge tone="ok" dot>
              In force
            </Badge>
          )}
        </span>
      ),
      when: <time dateTime={h.published_at} title={fmtWhen(h.published_at)}>{formatRelative(h.published_at)}</time>,
      body: (
        <div className="space-y-1.5">
          <div>
            {h.actor} · <span title={h.reason_code}>{reasonLabel(h.reason_code)}</span>
          </div>
          {prev ? (
            changes.length ? (
              <ul className="space-y-0.5 text-xs" aria-label={`Changes in version ${h.config_version}`}>
                {changes.map((c) => (
                  <li key={c.key}>
                    <span className="text-foreground">{c.label}</span>: {c.from} → {c.to}
                  </li>
                ))}
              </ul>
            ) : (
              <div className="text-xs">No term changes from version {prev.config_version}.</div>
            )
          ) : (
            <div className="text-xs">First recorded version: {headline(h.policy)}</div>
          )}
          <Button variant="link" size="xs" className="h-auto" onClick={() => setOpen(h)}>
            View terms of version {h.config_version}
          </Button>
        </div>
      ),
    };
  });

  return (
    <>
      <section aria-label="Checkout grace policy history">
        <Timeline items={items} emptyLabel="No version has been published yet." />
      </section>
      {history.length > PREVIEW && (
        <Button variant="secondary" size="sm" className="mt-4" onClick={() => setShowAll((v) => !v)}>
          {showAll ? `Show the latest ${PREVIEW} only` : `Show ${history.length - PREVIEW} older version${history.length - PREVIEW === 1 ? "" : "s"}`}
        </Button>
      )}

      <Sheet open={open !== null} onOpenChange={(v) => !v && setOpen(null)}>
        <SheetContent width="md">
          {open && (
            <>
              <SheetHeader
                eyebrow="Checkout grace history"
                icon={<History />}
                title={`Version ${open.config_version}`}
                description={`Published ${fmtWhen(open.published_at)} by ${open.actor}`}
                badges={
                  open.config_version === inForce ? (
                    <Badge tone="ok" dot>
                      In force
                    </Badge>
                  ) : (
                    <Badge tone="neutral">Superseded</Badge>
                  )
                }
              />
              <SheetBody>
                <SheetSection title="Why">
                  <KeyValueGrid
                    items={[
                      { label: "Reason", value: reasonLabel(open.reason_code), hint: open.reason_code || undefined },
                      { label: "Published by", value: open.actor },
                    ]}
                  />
                </SheetSection>
                {open.policy ? (
                  <>
                    <SheetSection title="What a departing guest received">
                      <p className="text-sm leading-relaxed">{guestReceivesSentence(open.policy)}</p>
                    </SheetSection>
                    <SheetSection title="Terms">
                      <section aria-label={`Version ${open.config_version} terms`}>
                        <KeyValueGrid
                          items={compareTerms(null, open.policy).map((c) => ({ label: c.label, value: c.to }))}
                        />
                      </section>
                    </SheetSection>
                  </>
                ) : (
                  <p className="text-sm text-muted-foreground">This version's terms were not recorded.</p>
                )}
              </SheetBody>
            </>
          )}
        </SheetContent>
      </Sheet>
    </>
  );
}
