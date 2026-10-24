"use client";

// Disabled-account retention bounds come from the appliance; changes require password and reason.

import type { AccountRetention } from "@/lib/api";
import { OperatorPolicyPanel } from "@/components/operator-policy-panel";

export function OperatorAccountRetention({ writable }: { writable: boolean }) {
  return (
    <OperatorPolicyPanel<AccountRetention>
      endpoint="/operators/account-retention"
      title="Disabled accounts"
      summary={(s) => `Anonymised ${s.disabled_account_days} days after being disabled`}
      description={
        <>
          A disabled account is kept so the activity log can still say who did what. After this period its email, name,
          password and authenticator are removed. An anonymised account cannot be enabled again.
        </>
      }
      saveLabel="Save disabled-account period"
      saved={{ title: "Saved", body: "Disabled accounts are anonymised after the new period from the next hourly run." }}
      writable={writable}
    />
  );
}
