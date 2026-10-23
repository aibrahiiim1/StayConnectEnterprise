"use client";

import { useState } from "react";
import { KeyRound } from "lucide-react";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
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
      <PageHeader
        icon={<KeyRound />}
        eyebrow="Your account"
        title="Change my password"
        description="Your current password is needed. Every other place you are signed in is signed out."
      />
      {done && <Callout tone="success" title="Password changed">{done}</Callout>}
      <Card>
        <CardBody>
          <ChangePasswordForm
            email={email ?? undefined}
            onChanged={(notice) => { setDone(notice); toast.success("Password changed"); }}
          />
        </CardBody>
      </Card>
    </PageShell>
  );
}
