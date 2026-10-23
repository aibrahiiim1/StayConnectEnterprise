"use client";

// Retired screen; the route stays so old bookmarks land here, and the edged API is kept.

import Link from "next/link";
import { Smartphone, ArrowUpRight } from "lucide-react";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";

export default function GuestDeviceSelfServiceRetiredPage() {
  return (
    <PageShell>
      <PageHeader
        icon={<Smartphone />}
        eyebrow="Clients"
        title="Client devices has moved"
        description="Client devices are managed in one place now: Devices."
      />
      <Card>
        <CardBody className="space-y-3 text-sm">
          <p>
            Clients see the devices using their package under <strong>Your devices</strong> on the portal, and can free
            the place of one of them — an online one is disconnected first — a limited number of times a day. That is
            always available; there is no switch for it.
          </p>
          <p>
            Staff find a device, disconnect it, release its place or make it sign in afresh on <strong>Devices</strong>,
            where the number of device changes a client may make per day is also set.
          </p>
          <Link href="/devices" className="inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline">
            Open Devices <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        </CardBody>
      </Card>
    </PageShell>
  );
}
