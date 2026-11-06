"use client";

import { useState } from "react";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Callout } from "@/components/ui/error-banner";
import { ChangePasswordForm } from "@/components/change-password-form";
import { useOperatorEmail } from "@/lib/whoami-context";
import { FADE } from "@/app/(app)/backups/load-failure";

export default function ChangeMyPasswordPage() {
  const email = useOperatorEmail();
  const [done, setDone] = useState<string | null>(null);
  return (
    <PageShell width="form">
      <PageHeader title="Change my password" description="Your current password is needed." />
      <Callout tone="info">Changing your password signs you out everywhere else. You stay signed in here.</Callout>
      {done && (
        <div className={FADE}>
          <Callout tone="success" title="Password changed">{done}</Callout>
        </div>
      )}
      <SectionCard title={email ?? "Your account"}>
        <ChangePasswordForm email={email ?? undefined} onChanged={setDone} />
      </SectionCard>
    </PageShell>
  );
}
