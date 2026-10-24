"use client";

// The code step, for an account that turned two-step sign-in on (D68). Turning it on happens on the account page.

import { useEffect, useId, useRef, useState } from "react";
import { ArrowLeft, Smartphone } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { api, ApiError } from "@/lib/api";

type CodeResult = { must_change_password?: boolean; recovery_codes_left?: number };

export function SigninSecondStep({ onDone, onRestart }: {
  onDone: () => void;
  onRestart: (why: string) => void;
}) {
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [codeErr, setCodeErr] = useState<string | null>(null);
  const [locked, setLocked] = useState<string | null>(null);
  const [lowOnCodes, setLowOnCodes] = useState<number | null>(null);
  const hintId = useId();

  const heading = useRef<HTMLHeadingElement | null>(null);
  useEffect(() => {
    if (lowOnCodes !== null) heading.current?.focus();
  }, [lowOnCodes]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null); setCodeErr(null); setLocked(null);
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
      if (e instanceof ApiError && (e.code === "wrong_code" || e.code === "code_required")) setCodeErr(e.message);
      else if (e instanceof ApiError && (e.status === 429 || e.code === "too_many_attempts")) setLocked(e.message);
      else setErr(e);
      setCode("");
    } finally {
      setBusy(false);
    }
  }

  if (lowOnCodes !== null) {
    return (
      <div className="space-y-4">
        <Callout tone="warning" title={<h2 ref={heading} tabIndex={-1} className="focus:outline-none">Recovery codes running low</h2>}>
          You signed in with a recovery code. {lowOnCodes === 0 ? "You have none left." : `You have ${lowOnCodes} left.`}{" "}
          Make a new set under Your account &gt; Two-step sign-in.
        </Callout>
        <Button type="button" variant="primary" size="lg" className="w-full" onClick={onDone}>Continue</Button>
      </div>
    );
  }

  const why = busy || codeErr ? null : code.trim() === "" ? "Enter your code to continue." : null;
  return (
    <form onSubmit={submit} className="space-y-4" noValidate>
      <ErrorBanner err={err} className="mb-0" />
      {locked && <Callout tone="warning" title="Too many wrong codes">{locked}</Callout>}
      <div className="flex items-start gap-3">
        <span className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-lg bg-accent text-muted-foreground" aria-hidden>
          <Smartphone className="size-4" />
        </span>
        <div className="min-w-0 space-y-0.5">
          <h2 className="text-emphasis">Enter your code</h2>
          <p className="text-caption text-muted-foreground">
            The 6-digit code your authenticator app shows now, or one of your recovery codes.
          </p>
        </div>
      </div>
      <Field label="Code" error={codeErr ?? undefined}>
        <Input
          // Focus the code field once it replaces the password form.
          // eslint-disable-next-line jsx-a11y/no-autofocus
          autoFocus
          required
          inputMode="text"
          autoComplete="one-time-code"
          spellCheck={false}
          maxLength={32}
          value={code}
          onChange={(e) => { setCode(e.target.value); setCodeErr(null); }}
          className="h-11 text-center font-mono text-lg tracking-[0.2em]"
        />
      </Field>
      <Button
        type="submit"
        loading={busy}
        disabled={code.trim() === ""}
        aria-describedby={why ? hintId : undefined}
        className="w-full"
        size="lg"
      >
        Sign in
      </Button>
      {why && <p id={hintId} className="-mt-2 text-center text-caption text-muted-foreground">{why}</p>}
      <Button type="button" variant="ghost" className="w-full" onClick={() => onRestart("")}>
        <ArrowLeft className="rtl:-scale-x-100" /> Back
      </Button>
    </form>
  );
}
