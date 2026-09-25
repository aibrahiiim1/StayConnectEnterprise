"use client";

// INTERNET PACKAGES — what a guest is offered, and what those offers actually did.
//
// Two views of one domain. PACKAGES is the catalogue: what each package gives, how many guests are on it now,
// and Add / Edit / Disable / Delete… from the package's record. GUEST ACTIVITY is the operational picture: every
// grant in a period — whether it came from the portal, a voucher, a guest account, a grace period or staff —
// with what it used, and the stay's own usage one click away.
//
// See packages-tab.tsx and activity-tab.tsx for the reasoning each view carries.

import { useCallback, useState } from "react";
import { Package, Plus } from "lucide-react";
import { ApiError } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
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

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<Package />}
        eyebrow="Internet offering"
        title="Internet packages"
        description="What guests are offered on the portal, and what those packages are doing for guests right now."
        actions={!disabled && (
          <Button onClick={() => { setTab("packages"); setAddRequest((n) => n + 1); }}>
            <Plus /> Add package
          </Button>
        )}
      />
      {disabled ? (
        <Card><CardBody>
          <EmptyState
            icon={<Package />}
            title="The internet offering is not switched on for this appliance"
            hint="Internet packages become available once this capability is enabled for the site. Contact your Velonet administrator."
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
            <PackagesTab guard={guard} setErr={setErr} addRequest={addRequest} onAddHandled={() => setAddRequest(0)} />
          </TabsContent>
          <TabsContent value="activity" className="mt-4">
            <ActivityTab guard={guard} setErr={setErr} />
          </TabsContent>
        </Tabs>
      )}
    </PageShell>
  );
}
