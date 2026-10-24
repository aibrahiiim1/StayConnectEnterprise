"use client";

// The code step, for an account that turned two-step sign-in on (D68). Turning it on happens on the account page.

import { useState } from "react";
import { KeyRound } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { api, ApiError } from "@/lib/api";

type CodeResult = { must_change_password?: boolean; recovery_codes_left?: number };

export function SigninSecondStep({ onDone, onRestart }: {
  onDone: () => void;
  onRestart: (why: string) => void;
}) {
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [lowOnCodes, setLowOnCodes] = useState<number | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setBusy(true);
    try {
      const r = await api.post<CodeResult>("/auth/login/code", { code: code.trim() });
      if (typeof r.recovery_codes_left === "number" && r.recovery_codes_left <= 3) {
        setLowOnCodes(r.recovery_codes_left);
        return;
      }
      onDone();
    } catch (e) {
      if (e instanceof ApiError && (e.code === "signin_expired" || e.code === "signin_restart")) {
        onRestart(e.message);
        return;
      }
      setErr(e);
      setCode("");
    } finally {
      setBusy(false);
    }
  }

  if (lowOnCodes !== null) {
    return (
      <div className="space-y-4">
        <p className="text-sm">
          You signed in with a recovery code. {lowOnCodes === 0 ? "You have none left." : `You have ${lowOnCodes} left.`}{" "}
          Make a new set under Your account &gt; Two-step sign-in.
        </p>
        <Button type="button" variant="primary" onClick={onDone}>Continue</Button>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div aria-live="assertive">
        <ErrorBanner err={err} className="mb-0" />
      </div>
      <div className="space-y-1.5">
        <h2 className="flex items-center gap-2 text-emphasis"><KeyRound className="size-4" aria-hidden />Enter your code</h2>
        <p className="text-sm text-muted-foreground">
          The 6-digit code your authenticator app shows now. Lost your phone? Enter one of your recovery codes.
        </p>
      </div>
      <Field label="Code">
        <Input
          // Restore focus to the code field after replacing the password form.
          // eslint-disable-next-line jsx-a11y/no-autofocus
          autoFocus
          required
          inputMode="text"
          autoComplete="one-time-code"
          maxLength={32}
          value={code}
          onChange={(e) => setCode(e.target.value)}
        />
      </Field>
      <Button type="submit" disabled={busy || code.trim() === ""} className="w-full" size="lg">
        {busy ? "Checking…" : "Sign in"}
      </Button>
      <Button type="button" variant="ghost" className="w-full" onClick={() => onRestart("")}>Back</Button>
    </form>
  );
}
