"use client";

// A SCREEN THE OPERATOR'S ROLE MAY NOT READ.
//
// The menu hides what a role cannot read, but every route still opens by URL or bookmark -- and used to render as
// an empty shell of 403 banners, which reads as a broken product rather than a permission (audit L-2). The layout
// now shows this instead, before the page mounts, so the page sends no request that would only be refused.
// edged still enforces every request; this only stops the console from pretending.

import Link from "next/link";
import { Lock } from "lucide-react";
import { Card, CardBody } from "@/components/ui/card";

export function NotPermitted({ label }: { label: string }) {
  return (
    <div className="mx-auto w-full max-w-3xl">
      <Card>
        <CardBody className="space-y-4 p-6">
          <div className="flex items-start gap-3">
            <Lock className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" aria-hidden />
            <div className="space-y-2">
              <h1 className="text-lg font-semibold tracking-tight">{label}</h1>
              <p className="text-sm font-medium">Your role does not include this screen</p>
              <p className="text-sm text-muted-foreground">
                Nothing is wrong with the appliance. If you need it, ask a site administrator to change your role
                under Operators.
              </p>
            </div>
          </div>
          <div className="border-t pt-4">
            <Link href="/dashboard" className="text-sm text-primary hover:underline">Back to the dashboard</Link>
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
