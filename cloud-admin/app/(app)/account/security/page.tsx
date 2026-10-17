"use client";

// TWO-STEP SIGN-IN — the operator's own authenticator (Central migration 0052, GDPR plan C10). The app is set up at
// sign-in, so this page shows the state and makes a new set of recovery codes (after the password step-up).
// Losing the phone is solved by a recovery code or by an administrator's reset, never from here: a stolen session
// plus a password must not be able to remove the second step.

import { useCallback, useEffect, useState } from "react";
import { api, withStepUp } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
import { formatDateTime } from "@/lib/status";

type MFAState = { enrolled: boolean; confirmed_at?: string; recovery_codes_left: number };

export default function TwoStepSigninPage() {
  const [state, setState] = useState<MFAState | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);

  const load = useCallback(async () => {
    try { setState(await api.get<MFAState>("/v1/auth/mfa")); } catch (e) { setErr(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function makeCodes() {
    setBusy(true); setErr(null);
    try {
      const r = await withStepUp(() => api.post<{ recovery_codes: string[] }>("/v1/auth/mfa/recovery-codes", {}));
      setCodes(r.recovery_codes);
      await load();
    } catch (e) { setErr(e); } finally { setBusy(false); }
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
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => void makeCodes()}>
              {busy ? "Making…" : "Make new recovery codes"}
            </Button>
          )}
        </CardBody>
      </Card>
    </PageShell>
  );
}
