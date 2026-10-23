"use client";

// Rotating recovery codes requires password step-up and a current authenticator code, never a recovery code.
// A stolen session and password must not remove the second factor.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError, withStepUp } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input, useFieldErrors } from "@/components/ui/input";
import { formatDateTime } from "@/lib/status";

type MFAState = { enrolled: boolean; confirmed_at?: string; recovery_codes_left: number };

export default function TwoStepSigninPage() {
  const [state, setState] = useState<MFAState | null>(null);
  const [err, setErr] = useState<unknown>(null);
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

  return (
    <PageShell width="narrow">
      <PageHeader title="Two-step sign-in" description="Signing in asks for your password and the code from your authenticator app." />
      <ErrorBanner err={err} />
      <Card>
        <CardHeader>
          <div className="flex w-full flex-wrap items-center justify-between gap-3">
            <CardTitle>Authenticator app</CardTitle>
            <Badge tone={state?.enrolled ? "ok" : "neutral"}>{state ? (state.enrolled ? "Set up" : "Not set up") : "—"}</Badge>
          </div>
        </CardHeader>
        <CardBody className="space-y-2">
          {state?.enrolled && state.confirmed_at && (
            <p className="text-caption text-muted-foreground">Set up {formatDateTime(state.confirmed_at)}.</p>
          )}
          <p className="text-sm text-muted-foreground">
            New phone? Ask an administrator to reset your two-step sign-in; you set up the app again the next time you
            sign in.
          </p>
        </CardBody>
      </Card>
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
          {codes && (
            <ul aria-label="New recovery codes" className="grid grid-cols-2 gap-2 rounded-lg border border-border bg-card p-3 font-mono text-sm">
              {codes.map((c) => <li key={c}>{c}</li>)}
            </ul>
          )}
          {state?.enrolled && (
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
          )}
        </CardBody>
      </Card>
    </PageShell>
  );
}
