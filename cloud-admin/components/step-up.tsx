"use client";

// PASSWORD CONFIRMATION, as a designed dialog.
//
// License, certificate and appliance actions are gated server-side: when the operator has not re-entered their
// password recently the API answers 403 reauth_required, and lib/api.ts withStepUp() asks for the password,
// re-authenticates and retries once. It used to ask with window.prompt, which shows the password in clear text
// and cannot say what it is for. This provider registers a real dialog in its place; the contract is the same.

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
    // The password never outlives the dialog that collected it.
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
        onSubmit={() => finish(password)}
      >
        <Field label="Password" required>
          <Input
            type="password"
            autoComplete="current-password"
            autoFocus
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
      </DialogForm>
    </>
  );
}
