"use client";

import type { AccountRetention } from "@/lib/api";
import { OperatorPolicyPanel } from "@/components/operator-policy-panel";
import { periodWords } from "@/lib/operator-errors";

export function OperatorAccountRetention({ writable }: { writable: boolean }) {
  return (
    <OperatorPolicyPanel<AccountRetention>
      endpoint="/operators/account-retention"
      title="Disabled accounts"
      summary={(s) => `Anonymised ${periodWords(s.disabled_account_days)} after being disabled`}
      description={
        <>
          A disabled account is kept so the activity log can still say who did what. After this period its email, name,
          password and authenticator are removed. An anonymised account cannot be enabled again.
        </>
      }
      saveLabel="Save disabled-account period"
      saved={{ title: "Disabled-account period saved", body: "Disabled accounts are anonymised after the new period from the next hourly run." }}
      writable={writable}
    />
  );
}
