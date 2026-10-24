"use client";

// New throttle settings affect the next attempt; active lockouts keep their expiry until password reset.

import type { SigninProtection } from "@/lib/api";
import { OperatorPolicyPanel } from "@/components/operator-policy-panel";

export function OperatorSigninProtection({ writable }: { writable: boolean }) {
  return (
    <OperatorPolicyPanel<SigninProtection>
      endpoint="/operators/signin-protection"
      title="Sign-in protection"
      summary={(s) => `${s.account_max_failures} wrong passwords lock an account for ${Math.round(s.lockout_seconds / 60)} min`}
      description={
        <>
          Protects this console&rsquo;s sign-in and every password confirmation. Wrong passwords are counted per account
          and per address; reaching a limit locks it for the lockout period, and every failure is recorded in Activity.
          Changes apply to the next attempt; an account already locked stays locked until that operator&rsquo;s password
          is reset.
        </>
      }
      saveLabel="Save sign-in protection"
      saved={{ title: "Sign-in protection saved", body: "It applies to the next sign-in attempt." }}
      writable={writable}
    />
  );
}
