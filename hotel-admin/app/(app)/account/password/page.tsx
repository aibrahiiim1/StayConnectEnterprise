"use client";

import { useState } from "react";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Callout } from "@/components/ui/error-banner";
import { ChangePasswordForm } from "@/components/change-password-form";
import { useToast } from "@/components/ui/toast";
import { useOperatorEmail } from "@/lib/whoami-context";

export default function ChangeMyPasswordPage() {
  const toast = useToast();
  const email = useOperatorEmail();
  const [done, setDone] = useState<string | null>(null);
  return (
    <PageShell width="form">
      <PageHeader title="Change my password" description="Your current password is needed." />
      <Callout tone="warning">Every other place you are signed in is signed out.</Callout>
      {done && <Callout tone="success" title="Password changed">{done}</Callout>}
      <SectionCard title={email ?? "Your account"}>
        <ChangePasswordForm
          email={email ?? undefined}
          onChanged={(notice) => { setDone(notice); toast.success("Password changed"); }}
        />
      </SectionCard>
    </PageShell>
  );
}
