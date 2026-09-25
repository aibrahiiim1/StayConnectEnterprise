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
        description="Whether this appliance is activated and connected to Velonet Central, and what its licence allows."
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
