"use client";

// THE PHASE-6 "CLIENT DEVICES" SCREEN IS RETIRED (decision D-K, audit C-1, 2026-10-16).
//
// It switched the dark Phase-6 guest device self-service on or off. Since migration 0118 the portal's "Your
// devices" list is served by scd's always-on client device routes and never reads that switch, so the screen saved
// a setting that controlled nothing -- and said "an online device can never be removed", which is no longer true.
// It also duplicated "Devices" in the menu.
//
// The edged API (/guest-device-self-service) is KEPT: it is the dark Phase-6 surface and its own deployment
// decision. This route stays only so an old bookmark lands on the answer rather than a 404.

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
