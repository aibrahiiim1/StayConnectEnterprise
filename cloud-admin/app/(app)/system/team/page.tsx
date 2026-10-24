"use client";

import { Card } from "@/components/ui/card";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { UsersManager } from "@/components/users-manager";
import { SessionLifetimeSetting } from "@/components/session-lifetime";
import { DataRetentionSetting } from "@/components/data-retention";
import { roleLabel, TEAM_ROLES, usePermissions } from "@/lib/permissions";
import { useSession } from "@/lib/session";

export default function TeamPage() {
  const { can } = usePermissions();
  const { me } = useSession();
  return (
    <PageShell>
      <PageHeader
        title="Team"
        description="The people who run Central, and the sign-in and retention rules that apply to them."
        help={
          <>
            <HelpSection title="Roles">
              <HelpList
                items={[
                  <><strong>{roleLabel("platform_admin")}</strong>: everything, including activation and licenses.</>,
                  <><strong>{roleLabel("platform_support")}</strong>: can see everything and change nothing.</>,
                  <>A customer&apos;s own users are managed on that customer&apos;s page.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Sign-in limits">
              <HelpList
                items={[
                  <>A sign-in ends after the set minutes without activity, and after the set hours however active the person is. A tab that is only open counts as inactive.</>,
                  <>A role change, disabling a person or resetting their password or two-step sign-in takes effect at once: their open sign-ins end.</>,
                  <>Repeated wrong passwords or authenticator codes lock the account for the lock time; the lock ends by itself.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Data retention">
              <p>
                Every hour, records older than the retention periods are deleted, or have the people in them removed.
                A shorter period removes older records at the next run. Every change needs a reason and is recorded in the
                audit log.
              </p>
            </HelpSection>
          </>
        }
      />
      <Card className="overflow-hidden">
        <UsersManager
          base="/cloud/v1/team"
          roles={TEAM_ROLES}
          canManage={can["team.manage"]}
          meId={me.operator_id}
          canResetPassword
          canExportData={can["team.manage"]}
          noun="team member"
        />
      </Card>
      <div className="grid gap-5 xl:grid-cols-2">
        <SessionLifetimeSetting canManage={can["team.manage"]} />
        <DataRetentionSetting canManage={can["team.manage"]} />
      </div>
    </PageShell>
  );
}
