"use client";

// Explain disabled capabilities and which guest services remain available.

import { Info } from "lucide-react";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { LinkButton } from "@/components/ui/button";
import { HelpSection } from "@/components/help";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";

export function SurfaceNotEnabled({ label }: { label: string }) {
  // Mention PMS only for hospitality sites.
  const hospitality = moduleLicensed(useCapabilities(), "hospitality");
  const unaffected = hospitality
    ? "client internet, sign-in, the PMS connection, sessions and accounting"
    : "client internet, sign-in, sessions and accounting";
  return (
    <PageShell width="narrow">
      <PageHeader
        title={label}
        helpTitle={label}
        help={
          <>
            <HelpSection title="Why this screen is empty">
              <p>
                The feature exists in OneGate but is not switched on for this site, so there is nothing here to show or
                to fix. None of {unaffected.replace(/ and accounting$/, " or accounting")} depends on this screen.
              </p>
            </HelpSection>
            <HelpSection title="Turning it on">
              <p>
                If this site should have it, ask Semantics support to enable it. It is a deployment decision rather than
                something an operator can turn on.
              </p>
            </HelpSection>
          </>
        }
      />
      <Card>
        <EmptyState
          icon={<Info />}
          title="Not enabled on this appliance"
          hint={
            <>
              This is a configuration of the appliance, not a fault.{" "}
              {unaffected.charAt(0).toUpperCase() + unaffected.slice(1)} are unaffected.
            </>
          }
          action={<LinkButton href="/dashboard" variant="secondary" size="sm">Back to the dashboard</LinkButton>}
        />
      </Card>
    </PageShell>
  );
}
