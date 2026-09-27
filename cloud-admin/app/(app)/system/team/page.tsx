"use client";

import { Card } from "@/components/ui/card";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { UsersManager } from "@/components/users-manager";
import { roleLabel, TEAM_ROLES, usePermissions } from "@/lib/permissions";
import { useSession } from "@/lib/session";

/** Central's own operators. A customer's users are managed on that customer's page, under Users. */
export default function TeamPage() {
  const { can } = usePermissions();
  const { me } = useSession();
  return (
    <PageShell>
      <PageHeader
        title="Team"
        description="The people who run Central. A customer's own users are managed on that customer's page."
        help={
          <HelpSection title="Roles">
            <HelpList
              items={[
                <><strong>{roleLabel("platform_admin")}</strong>: everything, including activation and licenses.</>,
                <><strong>{roleLabel("platform_support")}</strong>: can see everything and change nothing.</>,
                <>A role change takes effect the next time the person signs in.</>,
              ]}
            />
          </HelpSection>
        }
      />
      <Card>
        <UsersManager
          base="/cloud/v1/team"
          roles={TEAM_ROLES}
          canManage={can["team.manage"]}
          meId={me.operator_id}
          canResetPassword
          noun="team member"
        />
      </Card>
    </PageShell>
  );
}
