"use client";

import { useCallback, useEffect, useState } from "react";
import { Package, Plus } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { PackagesTab } from "./packages-tab";
import { ActivityTab } from "./activity-tab";
import { useOperatorRoles } from "@/lib/whoami-context";

type Tab = "packages" | "activity";

function useDisabled() {
  const [disabled, setDisabled] = useState(false);
  const guard = useCallback((e: unknown): boolean => {
    if (e instanceof ApiError && e.status === 503) { setDisabled(true); return true; }
    return false;
  }, []);
  return { disabled, guard };
}

export default function InternetPackagesPage() {
  const caps = useCapabilities();
  const hotel = moduleLicensed(caps, "hospitality");
  const paidAccess = moduleLicensed(caps, "paid_access");
  const [tab, setTab] = useState<Tab>("packages");
  const [err, setErr] = useState<string | null>(null);
  const [addRequest, setAddRequest] = useState(0);
  const { disabled, guard } = useDisabled();
  // Fails closed while loading; edged enforces the real gate either way.
  const roles = useOperatorRoles();
  const writable = roles !== null && canWrite("commercial-packages", roles);

  return (
    <PageShell width="wide">
      <PageHeader
        title="Internet packages"
        description="What clients are offered, and how it is being used."
        help={
          <>
            <HelpSection title="Two views">
              <HelpList items={[
                <><strong>Packages</strong> is the catalogue: what each package gives, how many clients are on it now, and Add, Edit, Disable or Delete from the package&rsquo;s record.</>,
                <><strong>Client activity</strong> is every grant in a period &mdash; from the portal, a voucher, a client account, After Check-out Access or staff &mdash; with what it used.</>,
              ]} />
            </HelpSection>
            <HelpSection title="Packages and service plans">
              <p>
                A service plan defines the speed, allowances and device limit a package hands out. Speed, data,
                time and device limits are changed on Service plans; a package shows them as context for the choice.
              </p>
              <p>Each saved change to a package is kept permanently. A client keeps the terms that applied when they connected.</p>
            </HelpSection>
            {hotel && (
              <HelpSection title="Data allowance per stay night">
                <p>
                  The allowance is worked out once, when the client is given the package, and does not change
                  afterwards if their stay is extended or shortened. Clients who did not sign in with their room
                  are not offered such a package, because their stay length is not known.
                </p>
              </HelpSection>
            )}
            <HelpSection title="Price">
              <p>
                {paidAccess
                  ? "A package with a price is acquired by the methods chosen for it, which are set up under Payment methods. A price of 0 is Free."
                  : "Packages are granted free or by voucher at this site; charging for a package needs the Paid access module."}
              </p>
            </HelpSection>
          </>
        }
        actions={!disabled && writable && (
          <Button onClick={() => { setTab("packages"); setAddRequest((n) => n + 1); }}>
            <Plus /> Add package
          </Button>
        )}
      />
      {!disabled && roles !== null && !writable && (
        <ReadOnlyNotice>Your role can see the internet packages but not change them.</ReadOnlyNotice>
      )}
      {disabled ? (
        <Card>
          <EmptyState
            icon={<Package />}
            title="The internet offering is not switched on for this appliance"
            hint="Internet packages become available once this capability is enabled for the site. Contact your Semantics administrator."
          />
        </Card>
      ) : (
        <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
          <TabsList>
            <TabsTrigger value="packages">Packages</TabsTrigger>
            <TabsTrigger value="activity">Client activity</TabsTrigger>
          </TabsList>
          <ErrorBanner err={err} className="mt-5" />
          <TabsContent value="packages" className="mt-5">
            <PackagesTab guard={guard} setErr={setErr} addRequest={addRequest} onAddHandled={() => setAddRequest(0)} writable={writable} />
          </TabsContent>
          <TabsContent value="activity" className="mt-5">
            <ActivityTab guard={guard} setErr={setErr} />
          </TabsContent>
        </Tabs>
      )}
    </PageShell>
  );
}
