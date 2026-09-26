"use client";

// APPLIANCE & LICENCE — one destination, because an operator has one question.
//
// There used to be two screens, "Activation" and "License", and both showed the licence state, both offered
// an upload, and both said whether the appliance was set up. They were not wrong to be two jobs; they were
// wrong to be two destinations. So this is one page with two tabs in the order the questions arrive: is the
// appliance connected and set up, and then what does its licence allow. The old routes redirect here with
// `?section=`, which picks the tab, because operators bookmark pages and a 404 is a worse answer than a move.

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { ServerCog, BadgeCheck } from "lucide-react";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { ApplianceSetupSection } from "./setup-section";
import { LicenseSection } from "./license-section";
import { HelpList, HelpSection } from "@/components/help";

type Section = "setup" | "license";

export default function ApplianceAndLicensePage() {
  const params = useSearchParams();
  const [section, setSection] = useState<Section>("setup");

  // A redirect from an old bookmark says which half the operator was looking for, so they land on it.
  useEffect(() => {
    const want = params.get("section");
    if (want === "license" || want === "setup") setSection(want);
  }, [params]);

  return (
    <PageShell>
      <PageHeader
        icon={<ServerCog />}
        eyebrow="System"
        title="Appliance & licence"
        description="Activation, the connection to OneGate Central, and what the licence allows."
        help={
          <>
            <HelpSection title="Appliance setup">
              <p>
                Activation brings this appliance online in OneGate Central. Choose <strong>Online</strong> (the
                default, nothing to type) or <strong>Offline</strong> (activate with a file). Claiming, assignment and
                certificates then happen on their own; the page follows along every few seconds.
              </p>
              <p>
                <strong>Advanced / recovery</strong> holds the enrollment token and the detailed onboarding checks for
                support. Secrets — the enrollment token, private keys, channel credentials — are never displayed.
              </p>
            </HelpSection>
            <HelpSection title="What the licence limits">
              <HelpList
                items={[
                  "The number of concurrent online guests across all guest networks.",
                  "The validity window, and the grace period after it ends.",
                  "A standard licence includes every product feature; per-feature entitlements exist in the signed format for future editions.",
                ]}
              />
            </HelpSection>
            <HelpSection title="When the licence is not in good standing">
              <p>
                New guest sign-ins are refused; guests already online are <strong>not</strong> dropped. DHCP, DNS, the
                sign-in page and this admin stay available.
              </p>
            </HelpSection>
            <HelpSection title="Renewing">
              <p>
                Semantics generates the licence file for this appliance&rsquo;s serial number and WAN MAC address. Upload
                it on the <strong>Licence</strong> tab. The appliance checks that it is bound to this exact hardware and
                refuses a licence older than the one installed, so a renewal can never roll you backwards.
              </p>
            </HelpSection>
            <HelpSection title="Connection to Central">
              <p>
                Central is used for licensing only: registration, certificates, the licence and its enforcement, and the
                signed customer/site binding. Guests are authorised by this appliance from its own data, so a Central
                outage does not interrupt service — it only delays licence renewal. The real-time channel is
                intentionally closed at this site; that is not a fault.
              </p>
            </HelpSection>
          </>
        }
      />

      <Tabs value={section} onValueChange={(v) => setSection(v as Section)}>
        <TabsList aria-label="Appliance and licence" className="overflow-x-auto">
          <TabsTrigger value="setup"><ServerCog className="size-4" aria-hidden /> Appliance setup</TabsTrigger>
          <TabsTrigger value="license"><BadgeCheck className="size-4" aria-hidden /> Licence</TabsTrigger>
        </TabsList>
        <TabsContent value="setup" className="mt-5">
          <ApplianceSetupSection />
        </TabsContent>
        <TabsContent value="license" className="mt-5">
          <LicenseSection />
        </TabsContent>
      </Tabs>
    </PageShell>
  );
}
