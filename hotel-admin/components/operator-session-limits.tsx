"use client";

// Session bounds come from the appliance; a change affects open sessions on their next request.

import type { SessionPolicy } from "@/lib/api";
import { OperatorPolicyPanel } from "@/components/operator-policy-panel";

export function OperatorSessionLimits({ writable }: { writable: boolean }) {
  return (
    <OperatorPolicyPanel<SessionPolicy>
      endpoint="/operators/session-policy"
      title="Session limits"
      summary={(s) => `Signed out after ${s.idle_timeout_minutes} min idle, ${s.max_session_hours} h at most`}
      description={
        <>
          A session ends after the idle limit, and any session ends at the maximum, however active. A tab that is only
          being looked at counts as idle. Changes apply to every open session, yours included, from its next request.
        </>
      }
      saveLabel="Save session limits"
      saved={{ title: "Session limits saved", body: "They apply to every open session from its next request." }}
      writable={writable}
    />
  );
}
