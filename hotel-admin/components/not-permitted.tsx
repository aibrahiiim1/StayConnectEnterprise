"use client";

// Check route permission before mounting the page to avoid sending requests the operator cannot make.

import { Lock } from "lucide-react";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { LinkButton } from "@/components/ui/button";

export function NotPermitted({ label }: { label: string }) {
  return (
    <PageShell width="narrow">
      <PageHeader title={label} />
      <Card>
        <EmptyState
          icon={<Lock />}
          title="Your role does not include this screen"
          hint="Nothing is wrong with the appliance. If you need it, ask a site administrator to change your role under Operators."
          action={<LinkButton href="/dashboard" variant="secondary" size="sm">Back to the dashboard</LinkButton>}
        />
      </Card>
    </PageShell>
  );
}
