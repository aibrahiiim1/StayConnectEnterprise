"use client";

// Publication history is append-only; Show more only collapses older versions.

import * as React from "react";
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
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { ChangeDiff, HistoryList } from "@/components/ui/history";
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

// Newest first, so the previous version is the next entry.
const previousOf = (history: GraceHistoryItem[], h: GraceHistoryItem) => history[history.indexOf(h) + 1];

function changeLine(history: GraceHistoryItem[], h: GraceHistoryItem): string {
  const prev = previousOf(history, h);
  if (!prev) return `First recorded version: ${headline(h.policy)}`;
  const changes = changedTerms(prev.policy, h.policy);
  if (changes.length === 0) return `No term changes from version ${prev.config_version}`;
  return changes.map((c) => `${c.label} ${c.from} → ${c.to}`).join(", ");
}

export function GraceHistory({ history, inForce }: { history: GraceHistoryItem[]; inForce: number }) {
  const [showAll, setShowAll] = React.useState(false);
  const [openVersion, setOpenVersion] = React.useState<number | null>(null);
  const shown = showAll ? history : history.slice(0, PREVIEW);
  const open = history.find((h) => h.config_version === openVersion) ?? null;
  const prev = open ? previousOf(history, open) : undefined;
  const changes = open && prev ? changedTerms(prev.policy, open.policy) : [];

  return (
    <>
      <section aria-label="Grace Period policy history">
        <HistoryList
          label="Published versions"
          selectedId={openVersion === null ? null : String(openVersion)}
          onSelect={(it) => setOpenVersion(Number(it.id))}
          emptyLabel="No version has been published yet."
          items={shown.map((h) => ({
            id: String(h.config_version),
            title: `Version ${h.config_version}`,
            badge: h.config_version === inForce ? <Badge tone="ok" dot size="sm">In force</Badge> : undefined,
            tone: h.config_version === inForce ? "ok" : "neutral",
            actor: `${h.actor} · ${reasonLabel(h.reason_code)}`,
            at: <time dateTime={h.published_at} title={fmtWhen(h.published_at)}>{formatRelative(h.published_at)}</time>,
            summary: changeLine(history, h),
          }))}
        />
      </section>
      {history.length > PREVIEW && (
        <div className="border-t border-border px-4 py-2.5">
          <Button variant="ghost" size="sm" onClick={() => setShowAll((v) => !v)}>
            {showAll ? `Show the latest ${PREVIEW} only` : `Show ${history.length - PREVIEW} older version${history.length - PREVIEW === 1 ? "" : "s"}`}
          </Button>
        </div>
      )}

      <DetailsDrawer
        open={open !== null}
        onOpenChange={(v) => !v && setOpenVersion(null)}
        eyebrow="Grace Period version"
        title={open ? `Version ${open.config_version}` : ""}
        description={open ? `Published ${fmtWhen(open.published_at)} by ${open.actor}` : undefined}
        badges={open ? (
          open.config_version === inForce
            ? <Badge tone="ok" dot>In force</Badge>
            : <Badge tone="neutral">Superseded</Badge>
        ) : undefined}
      >
        {open && (
          <>
            <DetailsSection title="Why">
              <DescriptionList
                items={[
                  { label: "Reason", value: reasonLabel(open.reason_code), hint: open.reason_code || undefined },
                  { label: "Published by", value: open.actor },
                ]}
              />
            </DetailsSection>
            <DetailsSection title={prev ? `Changes from version ${prev.config_version}` : "Changes"}>
              <section aria-label={`Changes in version ${open.config_version}`}>
                {prev ? (
                  <ChangeDiff
                    changes={changes.map((c) => ({ field: c.label, before: c.from, after: c.to }))}
                    beforeLabel={`Version ${prev.config_version}`}
                    afterLabel={`Version ${open.config_version}`}
                    emptyLabel={`No term changes from version ${prev.config_version}.`}
                  />
                ) : (
                  <p className="text-sm text-muted-foreground">The first recorded version.</p>
                )}
              </section>
            </DetailsSection>
            {open.policy ? (
              <>
                <DetailsSection title="What a departing guest received">
                  <p className="text-sm leading-relaxed">{guestReceivesSentence(open.policy)}</p>
                </DetailsSection>
                <DetailsSection title="Terms">
                  <section aria-label={`Version ${open.config_version} terms`}>
                    <DescriptionList
                      items={compareTerms(null, open.policy).map((c) => ({ label: c.label, value: c.to }))}
                    />
                  </section>
                </DetailsSection>
              </>
            ) : (
              <p className="text-sm text-muted-foreground">This version&rsquo;s terms were not recorded.</p>
            )}
          </>
        )}
      </DetailsDrawer>
    </>
  );
}
