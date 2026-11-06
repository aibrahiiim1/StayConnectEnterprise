"use client";

import type { SigninProtection } from "@/lib/api";
import { OperatorPolicyPanel } from "@/components/operator-policy-panel";
import { lockoutWords } from "@/lib/operator-errors";

export function OperatorSigninProtection({ writable }: { writable: boolean }) {
  return (
    <OperatorPolicyPanel<SigninProtection>
      endpoint="/operators/signin-protection"
      title="Sign-in protection"
      summary={(s) => lockoutWords(s.account_max_failures, s.lockout_seconds)}
      description={
        <>
          Wrong passwords are counted per account and per address; reaching a limit locks it for the lockout period.
          Changes apply to the next attempt. A locked account stays locked until the lockout ends or its password is
          reset.
        </>
      }
      saveLabel="Save sign-in protection"
      saved={{ title: "Sign-in protection saved", body: "It applies to the next sign-in attempt." }}
      writable={writable}
    />
  );
}
