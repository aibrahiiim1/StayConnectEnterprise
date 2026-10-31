"use client";

// The history is append-only; Show more only collapses older versions. Switch events sit in the same list.

import * as React from "react";
import {
  type AccessHistoryItem,
  changeLine,
  compareSnapshots,
  fmtWhen,
  isSwitchEvent,
  reasonLabel,
  termsAt,
} from "@/lib/api/after-checkout-access";
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { ChangeDiff, HistoryList } from "@/components/ui/history";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatRelative } from "@/lib/utils";

const PREVIEW = 5;

export function AccessHistory({ history, inForce }: { history: AccessHistoryItem[]; inForce: number }) {
  const [showAll, setShowAll] = React.useState(false);
  const [openVersion, setOpenVersion] = React.useState<number | null>(null);
  const shown = showAll ? history : history.slice(0, PREVIEW);
  const openIdx = history.findIndex((h) => h.config_version === openVersion);
  const open = openIdx >= 0 ? history[openIdx] : null;
  const switchEvent = open ? isSwitchEvent(open) : false;
  const before = open && !switchEvent ? termsAt(history, openIdx + 1) : null;
  const changes = open && !switchEvent ? compareSnapshots(before, open.policy).filter((c) => c.changed) : [];

  return (
    <>
      <section aria-label="After Check-out Access history">
        <HistoryList
          label="Versions"
          selectedId={openVersion === null ? null : String(openVersion)}
          onSelect={(it) => setOpenVersion(Number(it.id))}
          emptyLabel="Nothing has been saved yet."
          items={shown.map((h) => {
            const idx = history.indexOf(h);
            return {
              id: String(h.config_version),
              title: isSwitchEvent(h)
                ? `Version ${h.config_version} · ${h.policy?.after_checkout_enabled ? "Turned on" : "Turned off"}`
                : `Version ${h.config_version}`,
              badge: h.config_version === inForce ? <Badge tone="ok" dot size="sm">Latest</Badge> : undefined,
              tone: h.config_version === inForce ? "ok" : "neutral",
              actor: `${h.actor} · ${reasonLabel(h.reason_code)}`,
              at: <time dateTime={h.published_at} title={fmtWhen(h.published_at)}>{formatRelative(h.published_at)}</time>,
              summary: changeLine(history, idx),
            };
          })}
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
        eyebrow="After Check-out Access version"
        title={open ? `Version ${open.config_version}` : ""}
        description={open ? `Saved ${fmtWhen(open.published_at)} by ${open.actor}` : undefined}
        badges={open ? (
          open.config_version === inForce ? <Badge tone="ok" dot>Latest</Badge> : <Badge tone="neutral">Superseded</Badge>
        ) : undefined}
      >
        {open && (
          <>
            <DetailsSection title="Why">
              <DescriptionList
                items={[
                  { label: "Reason", value: reasonLabel(open.reason_code), hint: open.reason_code || undefined },
                  { label: "Saved by", value: open.actor },
                ]}
              />
            </DetailsSection>
            {switchEvent ? (
              <DetailsSection title="What changed">
                <p className="text-sm">
                  {open.policy?.after_checkout_enabled
                    ? "After Check-out Access was turned on."
                    : "After Check-out Access was turned off. Nobody is given access at check-out until it is turned on again."}
                </p>
              </DetailsSection>
            ) : (
              <>
                <DetailsSection title={before ? "Changes from the previous terms" : "Changes"}>
                  <section aria-label={`Changes in version ${open.config_version}`}>
                    {before ? (
                      <ChangeDiff
                        changes={changes.map((c) => ({ field: c.label, before: c.from, after: c.to }))}
                        beforeLabel="Before"
                        afterLabel={`Version ${open.config_version}`}
                        emptyLabel="No term changes."
                      />
                    ) : (
                      <p className="text-sm text-muted-foreground">The first recorded terms.</p>
                    )}
                  </section>
                </DetailsSection>
                {open.policy ? (
                  <DetailsSection title="Terms">
                    <section aria-label={`Version ${open.config_version} terms`}>
                      <DescriptionList items={compareSnapshots(null, open.policy).map((c) => ({ label: c.label, value: c.to }))} />
                    </section>
                  </DetailsSection>
                ) : (
                  <p className="text-sm text-muted-foreground">This version&rsquo;s terms were not recorded.</p>
                )}
              </>
            )}
          </>
        )}
      </DetailsDrawer>
    </>
  );
}
