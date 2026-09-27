"use client";

// APPLIANCE & LICENCE — one page. Activation, the licence and the connection to OneGate Central are three
// answers to one operator question, so they are one status card fed by one appliance endpoint
// (GET /central/status, docs/CENTRAL_CONTROL_PLANE.md section 8). The old /license, /network/cloud and
// /setup/enrollment addresses redirect here (next.config.mjs).

import { ServerCog } from "lucide-react";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { ApplianceStatus } from "./appliance-status";

export default function ApplianceAndLicencePage() {
  return (
    <PageShell>
      <PageHeader
        icon={<ServerCog />}
        eyebrow="System"
        title="Appliance & licence"
        description="Whether this appliance is activated, what its licence allows, and whether OneGate Central is reachable."
        help={
          <>
            <HelpSection title="Activation">
              <p>
                A new appliance registers itself with OneGate Central as soon as it has an internet connection, and
                keeps trying until it succeeds. Give your OneGate vendor the serial number shown here; once they
                activate it, this page shows <strong>Activated</strong> by itself within a minute or two.
              </p>
              <p>
                Without an internet connection, use <strong>Offline activation</strong>: download the activation
                request, send it to your vendor, and upload the activation package they return.
              </p>
            </HelpSection>
            <HelpSection title="What the licence limits">
              <HelpList
                items={[
                  "The number of guests online at the same time, across all guest networks.",
                  "The validity window, and the grace period after it ends.",
                ]}
              />
            </HelpSection>
            <HelpSection title="When the licence is not in good standing">
              <p>
                New guest sign-ins are refused; guests already online are <strong>not</strong> disconnected. DHCP,
                DNS, the sign-in page and this admin stay available.
              </p>
            </HelpSection>
            <HelpSection title="Renewing">
              <p>
                Renewals arrive by themselves while the appliance can reach OneGate Central. Your vendor can also send
                a licence file; upload it under <strong>Files from your OneGate vendor</strong>. The appliance checks
                that it was issued for this exact appliance and refuses an older licence, so a renewal can never roll
                you backwards.
              </p>
            </HelpSection>
            <HelpSection title="Connection to OneGate Central">
              <p>
                OneGate Central is used for activation and licensing only. Guests are signed in by this appliance from
                its own data, so when Central is unreachable guests are not affected — only licence renewals wait.
                <strong> Check now</strong> asks Central straight away and tests the connection.
              </p>
            </HelpSection>
          </>
        }
      />
      <ApplianceStatus />
    </PageShell>
  );
}
