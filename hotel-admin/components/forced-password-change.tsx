"use client";

// THE ONLY THING A MUST-CHANGE SESSION SEES (edged 0119).
//
// An operator who signed in with a weak password (a seeded admin/admin) or one an administrator chose for them
// is confined by edged to whoami, logout and the password change; every other API call answers
// password_change_required. So the console does not render its shell around a screen that cannot load: it
// shows this, full page, until the password is changed -- then reloads into the console proper.

import { ShieldAlert } from "lucide-react";
import { OneGateWordmark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/theme-toggle";
import { ChangePasswordForm } from "@/components/change-password-form";

export function ForcedPasswordChange({ email, onChanged, onLogout }: {
  email?: string;
  onChanged: () => void;
  onLogout: () => void;
}) {
  return (
    <div className="flex min-h-screen flex-col bg-background">
      <div className="flex items-center justify-between p-4 sm:p-6">
        <span className="inline-flex items-center rounded-lg bg-white px-3 py-1.5 shadow-control ring-1 ring-black/5">
          <OneGateWordmark className="text-[1.25rem]" />
        </span>
        <ThemeToggle />
      </div>
      <main className="flex flex-1 items-start justify-center px-4 pb-16 sm:px-6 sm:pt-8">
        <div className="w-full max-w-[28rem] space-y-5">
          <div className="space-y-2">
            <div className="flex items-center gap-2 text-warning-subtle-foreground">
              <ShieldAlert className="size-5" aria-hidden />
              <span className="text-label">Before you continue</span>
            </div>
            <h1 className="text-title">Set a new password</h1>
            <p className="text-sm text-muted-foreground">
              The password you signed in with{email ? <> for <strong className="font-medium text-foreground">{email}</strong></> : null} was
              set for you or is too short, so it must be replaced before the Admin Console can be used. Choose one
              only you know.
            </p>
          </div>
          <div className="rounded-lg border border-border bg-card p-5 shadow-control">
            <ChangePasswordForm email={email} submitLabel="Set new password" onChanged={() => onChanged()} />
          </div>
          <Button variant="ghost" onClick={onLogout}>Sign out instead</Button>
        </div>
      </main>
    </div>
  );
}
