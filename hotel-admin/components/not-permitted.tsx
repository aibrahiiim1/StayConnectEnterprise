"use client";

// Check route permission before mounting the page to avoid sending requests the operator cannot make.

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
