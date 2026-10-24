"use client";

// Two-step sign-in is optional per account and recommended (D68). Turning it on needs the password; turning it off,
// and making new recovery codes, need the password and a code, so a stolen session cannot remove or bypass it.
// Turning it on or off ends this operator's other sessions; the API renews this session's cookie, so the page only
// re-reads the state.

import { useCallback, useEffect, useState } from "react";
import { KeyRound } from "lucide-react";
import { PageShell, PageHeader } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm } from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/misc";
import { HelpList, HelpSection } from "@/components/help";
import { RecoveryCodes, TwoStepDisable, TwoStepEnable } from "@/components/two-step";
import { api, ApiError } from "@/lib/api";
import { formatDate } from "@/lib/utils";

type MFAState = { enrolled: boolean; confirmed_at?: string; recovery_codes_left: number };

export default function TwoStepSigninPage() {
  const [state, setState] = useState<MFAState | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [mode, setMode] = useState<"idle" | "enable" | "disable">("idle");
  const [notice, setNotice] = useState<string | null>(null);
  const [making, setMaking] = useState(false);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [codeErr, setCodeErr] = useState<string | null>(null);
  const [makeErr, setMakeErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);

  const load = useCallback(async () => {
    try { setState(await api.get<MFAState>("/auth/mfa")); setErr(null); } catch (e) { setErr(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const codeReady = /^\d{6}$/.test(code.replace(/\s/g, ""));

  function openMaking() {
    setPassword(""); setCode(""); setCodeErr(null); setMakeErr(null); setCodes(null); setNotice(null);
    setMaking(true);
  }

  async function makeCodes() {
    setBusy(true); setMakeErr(null); setCodeErr(null);
    try {
      const r = await api.post<{ recovery_codes: string[] }>("/auth/mfa/recovery-codes", { password, code });
      setCodes(r.recovery_codes);
      setMaking(false); setPassword(""); setCode("");
      await load();
    } catch (e) {
      if (e instanceof ApiError && (e.code === "wrong_code" || e.code === "code_required")) {
        setCodeErr(e.message); setCode("");
      } else {
        setMakeErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
      }
    } finally { setBusy(false); }
  }

  const on = state?.enrolled === true;
  const low = on && state !== null && state.recovery_codes_left <= 3;

  return (
    <PageShell width="form">
      <PageHeader
        title="Two-step sign-in"
        description="Ask for a code from your phone as well as your password when you sign in."
        help={
          <>
            <HelpSection title="How it works">
              <HelpList
                items={[
                  "Optional for each account, and recommended. With it on, signing in asks for your password and then the 6-digit code your authenticator app shows.",
                  "Any authenticator app works: Google Authenticator, Microsoft Authenticator, 1Password and others.",
                  "Someone who learns your password still cannot sign in without your phone.",
                ]}
              />
            </HelpSection>
            <HelpSection title="Recovery codes">
              <HelpList
                items={[
                  "Turning it on gives ten single-use recovery codes. Each signs you in once instead of the app.",
                  "Making a new set replaces every earlier code, and needs your password and a code from the app.",
                ]}
              />
            </HelpSection>
            <HelpSection title="A new phone">
              <HelpList
                items={[
                  "Turn two-step sign-in off with a recovery code, then on again with the new phone.",
                  "Or ask a site administrator to reset it on the Operators screen.",
                ]}
              />
            </HelpSection>
            <HelpSection title="Other sign-ins">
              <p>Turning it on or off signs you out everywhere else. You stay signed in here.</p>
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} className="mb-0" />
      {notice && <Callout tone="success">{notice}</Callout>}

      <SectionCard
        title="Authenticator app"
        description={
          state === null
            ? "Checking…"
            : on
              ? "Signing in asks for your password and a code from your app."
              : "Your password alone signs you in."
        }
        actions={state && <StatusBadge tone={on ? "ok" : "default"}>{on ? "On" : "Off"}</StatusBadge>}
      >
        {state === null ? (
          <div className="space-y-2" aria-hidden>
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-8 w-48" />
          </div>
        ) : mode === "enable" ? (
          <TwoStepEnable
            onCancel={() => setMode("idle")}
            onEnabled={() => void load()}
            onDone={() => {
              setMode("idle");
              setNotice("Two-step sign-in is on. Anywhere else you were signed in has been signed out.");
              void load();
            }}
          />
        ) : on ? (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-muted-foreground">
              {state.confirmed_at ? `Turned on ${formatDate(state.confirmed_at)}.` : "Turned on."}
            </p>
            <Button variant="secondary" onClick={() => { setMode("disable"); setNotice(null); }}>Turn off</Button>
          </div>
        ) : (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-muted-foreground">Recommended: protects this account if your password leaks.</p>
            <Button variant="primary" onClick={() => { setMode("enable"); setNotice(null); setCodes(null); }}>
              Turn on two-step sign-in
            </Button>
          </div>
        )}
      </SectionCard>

      {on && mode !== "enable" && (
        <SectionCard
          title="Recovery codes"
          description="Each signs you in once if you lose your phone."
          actions={<Badge tone={low ? "warn" : "neutral"}>{`${state!.recovery_codes_left} left`}</Badge>}
        >
          <div className="space-y-4">
            {low && !codes && (
              <Callout tone="warning">Make a new set before you run out.</Callout>
            )}
            {codes ? (
              <div className="space-y-3">
                <p className="text-sm text-muted-foreground">
                  Your earlier codes no longer work. These are shown only now.
                </p>
                <RecoveryCodes codes={codes} label="New recovery codes" />
                <div className="flex justify-end border-t border-border pt-4">
                  <Button variant="primary" onClick={() => setCodes(null)}>I have saved them</Button>
                </div>
              </div>
            ) : (
              <div className="flex flex-wrap items-center justify-between gap-3">
                <p className="text-sm text-muted-foreground">A new set replaces every earlier code.</p>
                <Button variant="secondary" onClick={openMaking}><KeyRound /> Make new recovery codes</Button>
              </div>
            )}
          </div>
        </SectionCard>
      )}

      <TwoStepDisable
        open={mode === "disable"}
        onOpenChange={(v) => { if (!v) setMode("idle"); }}
        onDone={() => {
          setMode("idle"); setCodes(null); setMaking(false);
          setNotice("Two-step sign-in is off. Your password alone signs you in, and anywhere else you were signed in has been signed out.");
          void load();
        }}
      />

      <DialogForm
        open={making}
        onOpenChange={(v) => { if (!v) setMaking(false); }}
        size="sm"
        title="Make new recovery codes"
        description="Every earlier recovery code stops working."
        submitLabel="Make codes"
        busyLabel="Making…"
        busy={busy}
        error={makeErr}
        disabled={password === "" || !codeReady}
        disabledReason="Enter your password and the 6-digit code to continue."
        onSubmit={makeCodes}
      >
        <Field label="Your password" required>
          <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
        <Field
          label="Code from your authenticator app"
          required
          hint="The 6 digits the app shows now. A recovery code cannot be used here."
          error={codeErr ?? undefined}
        >
          <Input
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={7}
            value={code}
            onChange={(e) => { setCode(e.target.value); setCodeErr(null); }}
            className="max-w-[10rem] font-mono tracking-widest"
          />
        </Field>
      </DialogForm>
    </PageShell>
  );
}
