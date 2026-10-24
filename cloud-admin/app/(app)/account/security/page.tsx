"use client";

// Two-step sign-in is optional per account and recommended (D68). Turning it on needs the password; turning it off,
// and making new recovery codes, need the password and a code, so a stolen session cannot remove or bypass it.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError, withStepUp } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input, useFieldErrors } from "@/components/ui/input";
import { RecoveryCodes, TwoStepDisable, TwoStepEnable } from "@/components/two-step";
import { formatDateTime } from "@/lib/status";

type MFAState = { enrolled: boolean; confirmed_at?: string; recovery_codes_left: number };

export default function TwoStepSigninPage() {
  const [state, setState] = useState<MFAState | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [mode, setMode] = useState<"idle" | "enable" | "disable">("idle");
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [code, setCode] = useState("");
  const f = useFieldErrors<"code">();

  const load = useCallback(async () => {
    try { setState(await api.get<MFAState>("/v1/auth/mfa")); } catch (e) { setErr(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function makeCodes() {
    setErr(null); f.clear();
    const c = code.replace(/[\s-]/g, "");
    if (!/^\d{6}$/.test(c)) {
      f.fail({ code: "Enter the 6-digit code your authenticator app shows now. A recovery code cannot be used here." });
      return;
    }
    setBusy(true);
    try {
      const r = await withStepUp(() => api.post<{ recovery_codes: string[] }>("/v1/auth/mfa/recovery-codes", { code: c }));
      setCodes(r.recovery_codes);
      setCode("");
      await load();
    } catch (e) {
      // Code errors belong to the field; service failures belong to the banner.
      if (e instanceof ApiError && (e.code === "wrong_code" || e.code === "code_required")) {
        setCode("");
        f.fail({ code: e.message });
      } else setErr(e);
    } finally { setBusy(false); }
  }

  const on = state?.enrolled === true;

  return (
    <PageShell width="narrow">
      <PageHeader
        title="Two-step sign-in"
        description="Optional and recommended: once on, signing in asks for your password and the code from your authenticator app."
      />
      <ErrorBanner err={err} />
      {notice && <p role="status" className="rounded-lg border border-border bg-surface px-3.5 py-3 text-sm">{notice}</p>}
      <Card>
        <CardHeader>
          <div className="flex w-full flex-wrap items-center justify-between gap-3">
            <CardTitle>Authenticator app</CardTitle>
            <Badge tone={on ? "ok" : "neutral"}>{state ? (on ? "On" : "Off") : "—"}</Badge>
          </div>
        </CardHeader>
        <CardBody className="space-y-3">
          {on && state?.confirmed_at && (
            <p className="text-caption text-muted-foreground">Turned on {formatDateTime(state.confirmed_at)}.</p>
          )}
          {state && !on && mode !== "enable" && (
            <>
              <p className="text-sm text-muted-foreground">
                Your password alone signs you in. With two-step sign-in on, someone who learns your password still
                cannot sign in without your phone.
              </p>
              <Button size="sm" variant="primary" onClick={() => { setMode("enable"); setNotice(null); }}>
                Turn on two-step sign-in
              </Button>
            </>
          )}
          {mode === "enable" && (
            <TwoStepEnable
              onCancel={() => setMode("idle")}
              onDone={() => { setMode("idle"); setNotice("Two-step sign-in is on. The next sign-in asks for the code from your app."); void load(); }}
            />
          )}
          {on && mode !== "disable" && (
            <>
              <p className="text-sm text-muted-foreground">
                New phone? Turn two-step sign-in off with a recovery code and on again, or ask an administrator to
                reset it.
              </p>
              <Button size="sm" variant="secondary" onClick={() => { setMode("disable"); setNotice(null); }}>Turn off</Button>
            </>
          )}
          {on && mode === "disable" && (
            <TwoStepDisable
              onCancel={() => setMode("idle")}
              onDone={() => { setMode("idle"); setCodes(null); setNotice("Two-step sign-in is off. Your password alone signs you in."); void load(); }}
            />
          )}
        </CardBody>
      </Card>
      {on && (
        <Card>
          <CardHeader>
            <div className="flex w-full flex-wrap items-center justify-between gap-3">
              <div className="space-y-0.5">
                <CardTitle>Recovery codes</CardTitle>
                <CardDescription>Each signs you in once without the app. A new set replaces every earlier code.</CardDescription>
              </div>
              <Badge tone={state && state.recovery_codes_left <= 3 ? "warn" : "neutral"}>
                {state ? `${state.recovery_codes_left} left` : "—"}
              </Badge>
            </div>
          </CardHeader>
          <CardBody className="space-y-3">
            {codes && <RecoveryCodes codes={codes} label="New recovery codes" />}
            <form
              className="space-y-3"
              noValidate
              onSubmit={(e) => { e.preventDefault(); void makeCodes(); }}
            >
              <Field
                label="Code from your authenticator app"
                hint="The 6-digit code the app shows now. You will also be asked for your password."
                error={f.error("code")}
              >
                <Input
                  id={f.id("code")}
                  name="code"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  pattern="[0-9]*"
                  maxLength={9}
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  className="max-w-[12rem] font-mono tracking-widest"
                />
              </Field>
              <Button type="submit" size="sm" variant="secondary" disabled={busy}>
                {busy ? "Making…" : "Make new recovery codes"}
              </Button>
            </form>
          </CardBody>
        </Card>
      )}
    </PageShell>
  );
}
