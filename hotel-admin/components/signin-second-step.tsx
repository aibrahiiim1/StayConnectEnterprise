"use client";

import { useEffect, useId, useRef, useState } from "react";
import { ArrowLeft, Smartphone } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { api, ApiError } from "@/lib/api";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const cleanCode = (v: string) => v.replace(/[\s-]/g, "");

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
  const codeRef = useRef<HTMLInputElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const autoSent = useRef("");
  useEffect(() => {
    if (lowOnCodes !== null) heading.current?.focus();
  }, [lowOnCodes]);
  useEffect(() => {
    codeRef.current?.focus();
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    setErr(null); setCodeErr(null); setLocked(null);
    setBusy(true);
    try {
      const r = await api.post<CodeResult>("/auth/login/code", { code: cleanCode(code) });
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
      autoSent.current = "";
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

  function onCode(v: string) {
    setCode(v);
    setCodeErr(null);
    const c = cleanCode(v);
    if (/^\d{6}$/.test(c) && autoSent.current !== c && !busy) {
      autoSent.current = c;
      requestAnimationFrame(() => formRef.current?.requestSubmit());
    }
  }

  const why = busy || codeErr ? null : cleanCode(code) === "" ? "Enter your code to continue." : null;
  return (
    <form ref={formRef} onSubmit={submit} className="space-y-4" noValidate>
      {err ? <div className={FADE}><ErrorBanner err={err} className="mb-0" /></div> : null}
      {locked && <Callout tone="warning" title="Too many wrong codes" className={FADE}>{locked}</Callout>}
      <div className="flex items-start gap-3">
        <span className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-lg bg-accent text-muted-foreground" aria-hidden>
          <Smartphone className="size-4" />
        </span>
        <div className="min-w-0 space-y-0.5">
          <h2 className="text-emphasis">Enter your code</h2>
          <p className="text-caption text-muted-foreground">
            The 6-digit code your authenticator app shows now, or one of your recovery codes. It signs in as soon as
            6 digits are in.
          </p>
        </div>
      </div>
      <Field label="Code" error={codeErr ?? undefined}>
        <Input
          ref={codeRef}
          required
          inputMode="text"
          autoComplete="one-time-code"
          spellCheck={false}
          maxLength={32}
          value={code}
          onChange={(e) => onCode(e.target.value)}
          className="h-11 text-center font-mono text-lg tracking-[0.2em]"
        />
      </Field>
      <Button
        type="submit"
        loading={busy}
        disabled={cleanCode(code) === ""}
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
