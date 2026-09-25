"use client";

// APPLIANCE & LICENCE — one destination, because an operator has one question.
//
// THE PROBLEM THIS REPLACES. There were two screens. "Activation" was where an appliance got connected and
// recovered; "License" was where its licence lived. Both showed the licence state. Both offered a licence
// upload. Both told you whether the appliance was set up. And an operator arriving with "is this thing
// working and is it licensed?" had to decide which one to open before they could find out — a decision the
// product invented and then made them take, every time.
//
// They were not wrong to be two jobs. They were wrong to be two destinations. So this is one page with two
// sections in the order the questions arrive: is the appliance connected and set up, and then what does its
// licence allow. The old routes redirect here, because operators bookmark pages and a 404 is a worse answer
// than a move.
//
// ONE LICENCE UPLOAD. The offline upload lives in Appliance setup, which is where recovery belongs; the
// Licence section shows state, capacity and expiry and points at it rather than offering a second one.

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { ServerCog, BadgeCheck } from "lucide-react";
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
    <div className="mx-auto w-full max-w-4xl space-y-5">
      <header>
        <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">System</div>
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight sm:text-2xl">
          <ServerCog className="h-5 w-5" /> Appliance &amp; licence
        </h1>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
          Whether this appliance is connected to Velonet and set up, and what its licence allows.
        </p>
      </header>

      <div className="flex flex-wrap gap-1 border-b" role="tablist" aria-label="Appliance and licence">
        {([["setup", "Appliance setup", ServerCog], ["license", "Licence", BadgeCheck]] as const).map(([id, label, Icon]) => (
          <button key={id} role="tab" type="button" aria-selected={section === id}
            onClick={() => setSection(id)}
            className={`-mb-px inline-flex items-center gap-2 border-b-2 px-4 py-2.5 text-sm ${
              section === id ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground"
            }`}>
            <Icon className="h-4 w-4" /> {label}
          </button>
        ))}
      </div>

      {section === "setup" ? <ApplianceSetupSection /> : <LicenseSection />}
    </div>
  );
}
