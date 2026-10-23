"use client";

// Sensitive writes require a recent password; this masked dialog supplies it for one retry.

import * as React from "react";
import { setStepUpPrompter } from "@/lib/api";
import { DialogForm } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/input";

type Pending = { message: string; resolve: (pw: string | null) => void };

export function StepUpProvider({ children }: { children: React.ReactNode }) {
  const [pending, setPending] = React.useState<Pending | null>(null);
  const [password, setPassword] = React.useState("");

  React.useEffect(
    () =>
      setStepUpPrompter(
        (message) =>
          new Promise<string | null>((resolve) => {
            setPassword("");
            setPending({ message, resolve });
          }),
      ),
    [],
  );

  function finish(value: string | null) {
    pending?.resolve(value);
    setPending(null);
    // Clear the password when the dialog closes.
    setPassword("");
  }

  return (
    <>
      {children}
      <DialogForm
        open={!!pending}
        onOpenChange={(open) => {
          if (!open) finish(null);
        }}
        title="Confirm your password"
        description={pending?.message}
        size="sm"
        submitLabel="Confirm"
        disabled={password === ""}
        disabledReason="Enter your password to continue."
        onSubmit={() => finish(password)}
      >
        <Field label="Password" required>
          <Input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
      </DialogForm>
    </>
  );
}
