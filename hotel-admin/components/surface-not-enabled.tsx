"use client";

// Explain disabled capabilities and which guest services remain available.

import Link from "next/link";
import { Info } from "lucide-react";
import { Card, CardBody } from "@/components/ui/card";
import { HelpSection, HelpTip } from "@/components/help";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";

export function SurfaceNotEnabled({ label }: { label: string }) {
  // Mention PMS only for hospitality sites.
  const hospitality = moduleLicensed(useCapabilities(), "hospitality");
  const unaffected = hospitality
    ? "client internet, sign-in, the PMS connection, sessions and accounting"
    : "client internet, sign-in, sessions and accounting";
  return (
    <div className="mx-auto w-full max-w-3xl">
      <Card>
        <CardBody className="space-y-4 p-6">
          <div className="flex items-start gap-3">
            <Info className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
            <div className="space-y-2">
              <div className="flex items-center gap-2">
                <h1 className="text-lg font-semibold tracking-tight">{label}</h1>
                <HelpTip title={label}>
                  <HelpSection title="Why this screen is empty">
                    <p>
                      The feature exists in OneGate but is not switched on for this site, so there is nothing
                      here to show or to fix. None of {unaffected.replace(/ and accounting$/, " or accounting")}{" "}
                      depends on this screen.
                    </p>
                  </HelpSection>
                  <HelpSection title="Turning it on">
                    <p>
                      If this site should have it, ask Semantics support to enable it. It is a deployment decision
                      rather than something an operator can turn on.
                    </p>
                  </HelpSection>
                </HelpTip>
              </div>
              <p className="text-sm font-medium">Not enabled on this appliance</p>
              <p className="text-sm text-muted-foreground">
                This is a configuration of the appliance, not a fault.{" "}
                {unaffected.charAt(0).toUpperCase() + unaffected.slice(1)} are unaffected.
              </p>
            </div>
          </div>
          <div className="border-t pt-4">
            <Link href="/dashboard" className="text-sm text-primary hover:underline">
              Back to the dashboard
            </Link>
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
