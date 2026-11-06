"use client";

import { useRef, useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";

export const MIN_OPERATOR_PASSWORD = 10;

const NOT_CHANGED = "Your password was not changed.";

export function passwordChangeError(e: unknown): string {
  const o = (e ?? {}) as { status?: unknown; code?: unknown; message?: unknown };
  const code = typeof o.code === "string" ? o.code : "";
  const text = typeof o.message === "string" ? o.message.trim() : "";
  if (code === "unauthenticated") return `Your session has ended. Sign in again. ${NOT_CHANGED}`;
  if (code === "bad_request") return `Fill in all three fields. ${NOT_CHANGED}`;
  if (/^[A-Z]/.test(text) && /[.!?]$/.test(text)) return /not (been )?changed/i.test(text) ? text : `${text} ${NOT_CHANGED}`;
  if (o.status === 429) return `Too many attempts. Wait a minute, then try again. ${NOT_CHANGED}`;
  return "Your password could not be changed. Try again in a moment. Your old password still works.";
}

type SecretProps = {
  id?: string;
  value: string;
  onChange: (v: string) => void;
  autoComplete: string;
  inputRef?: React.Ref<HTMLInputElement>;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
  "aria-required"?: boolean;
};

function Secret({ id, value, onChange, autoComplete, inputRef, ...aria }: SecretProps) {
  const [shown, setShown] = useState(false);
  return (
    <div className="relative">
      <Input
        ref={inputRef}
        id={id}
        {...aria}
        type={shown ? "text" : "password"}
        autoComplete={autoComplete}
        autoCapitalize="none"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="pe-10"
      />
      <button
        type="button"
        onClick={() => setShown((s) => !s)}
        aria-label={shown ? "Hide password" : "Show password"}
        aria-pressed={shown}
        className="absolute inset-y-0 end-0 flex w-10 items-center justify-center rounded-e-md text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&_svg]:size-4"
      >
        {shown ? <EyeOff aria-hidden /> : <Eye aria-hidden />}
      </button>
    </div>
  );
}

export function ChangePasswordForm({ email, onChanged, submitLabel = "Change password" }: {
  email?: string;
  onChanged: (notice: string) => void;
  submitLabel?: string;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const currentRef = useRef<HTMLInputElement>(null);

  const problem =
    next === "" ? null
    : next.length < MIN_OPERATOR_PASSWORD ? `${MIN_OPERATOR_PASSWORD - next.length} more characters needed.`
    : email && next.trim().toLowerCase() === email.trim().toLowerCase() ? "It must not be your account name."
    : next === current ? "It must be different from your current password."
    : null;
  const mismatch = again !== "" && again !== next;
  const ready = current !== "" && next !== "" && !problem && again === next && !busy;
  const missing =
    current === "" ? "Type your current password."
    : next === "" ? "Choose a new password."
    : again === "" && !problem ? "Type the new password again."
    : null;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!ready) return;
    setBusy(true); setErr(null);
    try {
      const r = await api.post<{ notice?: string }>("/auth/password", { current_password: current, new_password: next });
      setCurrent(""); setNext(""); setAgain("");
      onChanged(r?.notice ?? "Your password was changed.");
    } catch (e2) {
      setErr(passwordChangeError(e2));
      const code = (e2 as { code?: unknown } | null)?.code;
      if (code === "reauth_required") {
        setCurrent("");
        currentRef.current?.focus();
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4" noValidate>
      <ErrorBanner err={err} className="mb-0" />
      <Field label="Current password" required>
        <Secret inputRef={currentRef} autoComplete="current-password" value={current} onChange={setCurrent} />
      </Field>
      <Field label="New password" required
        hint={`At least ${MIN_OPERATOR_PASSWORD} characters, and not your account name.`}
        error={problem ?? undefined}>
        <Secret autoComplete="new-password" value={next} onChange={setNext} />
      </Field>
      <Field label="Type the new password again" required error={mismatch ? "These do not match." : undefined}>
        <Secret autoComplete="new-password" value={again} onChange={setAgain} />
      </Field>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Button type="submit" disabled={!ready} className="w-full sm:w-auto">
          {busy ? "Changing…" : submitLabel}
        </Button>
        {missing && !busy && <span className="text-caption text-muted-foreground">{missing}</span>}
      </div>
    </form>
  );
}
