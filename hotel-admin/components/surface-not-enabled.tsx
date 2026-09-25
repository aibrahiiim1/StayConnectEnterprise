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

export function SurfaceNotEnabled({ label }: { label: string }) {
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
              <h1 className="text-lg font-semibold tracking-tight">{label}</h1>
              <p className="text-sm font-medium">Not enabled on this appliance</p>
              <p className="text-sm text-muted-foreground">
                This is a configuration of the appliance, not a fault. The feature exists in OneGate but
                is not switched on for this property, so there is nothing here to show or to fix.
              </p>
              <p className="text-sm text-muted-foreground">
                Guest internet, sign-in, the PMS connection, sessions and accounting are unaffected — none of
                them depends on this screen.
              </p>
              <p className="text-sm text-muted-foreground">
                If this property should have it, ask Semantics support to enable it; it is a deployment
                decision rather than something an operator can turn on.
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
