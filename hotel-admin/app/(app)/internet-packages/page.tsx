"use client";

// INTERNET PACKAGES — what a guest is offered, and what those offers actually did.
//
// Two views of one domain. PACKAGES is the catalogue: what each package gives, how many guests are on it now,
// and Add / Edit / Disable / Delete… from the package's record. GUEST ACTIVITY is the operational picture: every
// grant in a period — whether it came from the portal, a voucher, a guest account, a grace period or staff —
// with what it used, and the stay's own usage one click away.
//
// See packages-tab.tsx and activity-tab.tsx for the reasoning each view carries.

import { useCallback, useEffect, useState } from "react";
import { Package, Plus } from "lucide-react";
import { api, ApiError, Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { PackagesTab } from "./packages-tab";
import { ActivityTab } from "./activity-tab";

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
  const [tab, setTab] = useState<Tab>("packages");
  const [err, setErr] = useState<string | null>(null);
  const [addRequest, setAddRequest] = useState(0);
  const { disabled, guard } = useDisabled();
  // Fails closed while it loads: the Add / Edit / Disable / Delete controls appear only for a role the server
  // will accept them from. edged enforces the real gate either way.
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const writable = roles !== null && canWrite("commercial-packages", roles);

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<Package />}
        eyebrow="Internet offering"
        title="Internet packages"
        description="What guests are offered, and how it is being used."
        help={
          <>
            <HelpSection title="Two views">
              <HelpList items={[
                <><strong>Packages</strong> is the catalogue: what each package gives, how many guests are on it now, and Add, Edit, Disable or Delete from the package&rsquo;s record.</>,
                <><strong>Guest activity</strong> is every grant in a period &mdash; from the portal, a voucher, a guest account, a grace period or staff &mdash; with what it used.</>,
              ]} />
            </HelpSection>
            <HelpSection title="Packages and service plans">
              <p>
                A service plan defines the speed, allowances and device limit a package hands out. Speed, data,
                time and device limits are changed on Service plans; a package shows them as context for the choice.
              </p>
              <p>Each saved change to a package is kept permanently. A guest keeps the terms that applied when they connected.</p>
            </HelpSection>
            <HelpSection title="Data allowance per stay night">
              <p>
                The allowance is worked out once, when the guest is given the package, and does not change
                afterwards if their stay is extended or shortened. Guests who did not sign in with their room
                are not offered such a package, because their stay length is not known.
              </p>
            </HelpSection>
            <HelpSection title="Price">
              <p>
                Selling packages to guests is not enabled on this appliance, so there is no price to set; a
                package is granted rather than sold.
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
        <Card><CardBody>
          <EmptyState
            icon={<Package />}
            title="The internet offering is not switched on for this appliance"
            hint="Internet packages become available once this capability is enabled for the site. Contact your Semantics administrator."
          />
        </CardBody></Card>
      ) : (
        <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
          <TabsList>
            <TabsTrigger value="packages">Packages</TabsTrigger>
            <TabsTrigger value="activity">Guest activity</TabsTrigger>
          </TabsList>
          <ErrorBanner err={err} className="mt-4" />
          <TabsContent value="packages" className="mt-4">
            <PackagesTab guard={guard} setErr={setErr} addRequest={addRequest} onAddHandled={() => setAddRequest(0)} writable={writable} />
          </TabsContent>
          <TabsContent value="activity" className="mt-4">
            <ActivityTab guard={guard} setErr={setErr} />
          </TabsContent>
        </Tabs>
      )}
    </PageShell>
  );
}
