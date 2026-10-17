"use client";

// TWO-STEP SIGN-IN — the operator's own authenticator (migration 0132, GDPR plan C10). Reached from the account
// area at the foot of the sidebar. The app is set up at sign-in, so this page shows the state and makes a new set
// of recovery codes. Losing the phone is solved by a recovery code or by a site administrator's reset on the
// Operators screen, never from here: a stolen session plus a password must not be able to remove the second step.

import { useCallback, useEffect, useState } from "react";
import { Smartphone } from "lucide-react";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
import { api, ApiError } from "@/lib/api";
import { formatDate } from "@/lib/utils";

type MFAState = { enrolled: boolean; confirmed_at?: string; recovery_codes_left: number };

export default function TwoStepSigninPage() {
  const [state, setState] = useState<MFAState | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [making, setMaking] = useState(false);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);

  const load = useCallback(async () => {
    try { setState(await api.get<MFAState>("/auth/mfa")); } catch (e) { setErr(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function makeCodes() {
    setBusy(true); setErr(null);
    try {
      const r = await api.post<{ recovery_codes: string[] }>("/auth/mfa/recovery-codes", { password });
      setCodes(r.recovery_codes); setMaking(false); setPassword("");
      await load();
    } catch (e) {
      setErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
    } finally { setBusy(false); }
  }

  return (
    <PageShell width="form">
      <PageHeader
        icon={<Smartphone />}
        eyebrow="Your account"
        title="Two-step sign-in"
        description="Signing in asks for your password and the code from your authenticator app."
      />
      <ErrorBanner err={err} />
      <Card>
        <CardBody className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="text-emphasis">Authenticator app</div>
            <Badge tone={state?.enrolled ? "ok" : "neutral"}>{state ? (state.enrolled ? "Set up" : "Not set up") : "—"}</Badge>
          </div>
          {state?.enrolled && state.confirmed_at && (
            <p className="text-caption text-muted-foreground">Set up {formatDate(state.confirmed_at)}.</p>
          )}
          <p className="text-sm text-muted-foreground">
            New phone? Ask a site administrator to reset your two-step sign-in on the Operators screen; you set up
            the app again the next time you sign in.
          </p>
        </CardBody>
      </Card>
      <Card>
        <CardBody className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="text-emphasis">Recovery codes</div>
            <Badge tone={state && state.recovery_codes_left <= 3 ? "warn" : "neutral"}>
              {state ? `${state.recovery_codes_left} left` : "—"}
            </Badge>
          </div>
          <p className="text-sm text-muted-foreground">
            Each signs you in once without the app. Making a new set replaces every earlier code.
          </p>
          {codes && (
            <ul aria-label="New recovery codes" className="grid grid-cols-2 gap-2 rounded-lg border border-border bg-card p-3 font-mono text-sm">
              {codes.map((c) => <li key={c}>{c}</li>)}
            </ul>
          )}
          {state?.enrolled && !making && (
            <Button size="sm" variant="outline" onClick={() => { setMaking(true); setCodes(null); }}>Make new recovery codes</Button>
          )}
          {making && (
            <div className="flex flex-wrap items-end gap-2">
              <Field label="Your password" required hint="Confirms the change.">
                <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
              </Field>
              <Button size="sm" variant="primary" disabled={busy || password === ""} onClick={() => void makeCodes()}>
                {busy ? "Making…" : "Make codes"}
              </Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => setMaking(false)}>Cancel</Button>
            </div>
          )}
        </CardBody>
      </Card>
    </PageShell>
  );
}
