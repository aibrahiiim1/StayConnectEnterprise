"use client";

// Retired screen; the route stays so old bookmarks land here, and the edged API is kept.

import Link from "next/link";
import { ArrowUpRight } from "lucide-react";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";

export default function GuestDeviceSelfServiceRetiredPage() {
  return (
    <PageShell>
      <PageHeader
        title="Client devices has moved"
        description="Client devices are managed in one place now: Devices."
      />
      <Card>
        <CardBody className="space-y-3 text-sm">
          <p>
            Clients manage their own devices under <strong>Your devices</strong> on the portal. Staff find, disconnect
            or release a device on <strong>Devices</strong>, where the daily limit on client device changes is also set.
          </p>
          <Link href="/devices" className="inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline">
            Open Devices <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        </CardBody>
      </Card>
    </PageShell>
  );
}
