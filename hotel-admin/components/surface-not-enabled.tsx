"use client";

// A FEATURE THAT IS NOT ENABLED HERE IS NOT A BROKEN PRODUCT.
//
// Eight destinations on the PRE-LIVE appliance rendered a blank page and a console full of 404s, because the
// bundle was built with every capability compiled in and this appliance mounts a subset. Nothing on screen
// said so. An operator cannot tell "this hotel does not have that" from "this is broken", and from where
// they are standing the second reading is the reasonable one.
//
// This is what those screens say instead. It names the feature, says plainly that it is not enabled on this
// appliance, and — the part that matters — says what IS unaffected, because the question behind every
// unexpected screen in an admin is whether guests are all right.

import Link from "next/link";
import { Info } from "lucide-react";
import { Card, CardBody } from "@/components/ui/card";
import { HelpSection, HelpTip } from "@/components/help";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";

export function SurfaceNotEnabled({ label }: { label: string }) {
  // WHAT IS UNAFFECTED, in this site's terms. The PMS connection is named only where hospitality is licensed:
  // reassuring an office that its PMS is fine tells it the product thinks it is a hotel.
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
              {/* Two lines, not one sentence. "Guest devices is not enabled" and "Settlements is not
                  enabled" are both wrong, and a product that cannot agree with its own labels reads as
                  careless at exactly the moment an operator is already wondering whether it is broken. */}
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
