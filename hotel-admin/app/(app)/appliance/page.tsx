"use client";

import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { ApplianceStatus } from "./appliance-status";

const HELP = (
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
          "The number of clients online at the same time, across all client networks.",
          "The validity window, and the grace period after it ends.",
        ]}
      />
    </HelpSection>
    <HelpSection title="When the licence is not in good standing">
      <p>
        New client sign-ins are refused; clients already online are <strong>not</strong> disconnected. DHCP,
        DNS, the sign-in page and this admin stay available.
      </p>
    </HelpSection>
  </>
);

export default function ApplianceAndLicencePage() {
  return (
    <PageShell>
      <ApplianceStatus
        header={(actions) => (
          <PageHeader
            title="Appliance & licence"
            description="Activation, what the licence allows, and whether OneGate Central is reachable."
            help={HELP}
            actions={actions}
          />
        )}
      />
    </PageShell>
  );
}
