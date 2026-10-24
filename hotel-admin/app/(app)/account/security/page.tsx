"use client";

// Two-step sign-in is optional per account and recommended (D68). Turning it on needs the password; turning it off,
// and making new recovery codes, need the password and a code, so a stolen session cannot remove or bypass it.

import { useCallback, useEffect, useState } from "react";
import { Smartphone } from "lucide-react";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
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
  const [busy, setBusy] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);

  const load = useCallback(async () => {
    try { setState(await api.get<MFAState>("/auth/mfa")); } catch (e) { setErr(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const codeReady = /^\d{6}$/.test(code.replace(/\s/g, ""));

  function stopMaking() {
    setMaking(false); setPassword(""); setCode(""); setCodeErr(null);
  }

  async function makeCodes() {
    setBusy(true); setErr(null); setCodeErr(null);
    try {
      const r = await api.post<{ recovery_codes: string[] }>("/auth/mfa/recovery-codes", { password, code });
      setCodes(r.recovery_codes); stopMaking();
      await load();
    } catch (e) {
      if (e instanceof ApiError && (e.code === "wrong_code" || e.code === "code_required")) {
        setCodeErr(e.message); setCode("");
      } else {
        setErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
      }
    } finally { setBusy(false); }
  }

  const on = state?.enrolled === true;

  return (
    <PageShell width="form">
      <PageHeader
        icon={<Smartphone />}
        eyebrow="Your account"
        title="Two-step sign-in"
        description="Optional and recommended: once on, signing in asks for your password and the code from your authenticator app."
      />
      <ErrorBanner err={err} />
      {notice && <p role="status" className="mb-4 rounded-lg border border-border bg-surface px-3.5 py-3 text-sm">{notice}</p>}
      <Card>
        <CardBody className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="text-emphasis">Authenticator app</div>
            <Badge tone={on ? "ok" : "neutral"}>{state ? (on ? "On" : "Off") : "—"}</Badge>
          </div>
          {on && state?.confirmed_at && (
            <p className="text-caption text-muted-foreground">Turned on {formatDate(state.confirmed_at)}.</p>
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
                New phone? Turn two-step sign-in off with a recovery code and on again, or ask a site administrator to
                reset it on the Operators screen.
              </p>
              <Button size="sm" variant="outline" onClick={() => { setMode("disable"); setNotice(null); }}>
                Turn off
              </Button>
            </>
          )}
          {on && mode === "disable" && (
            <TwoStepDisable
              onCancel={() => setMode("idle")}
              onDone={() => { setMode("idle"); setCodes(null); stopMaking(); setNotice("Two-step sign-in is off. Your password alone signs you in."); void load(); }}
            />
          )}
        </CardBody>
      </Card>
      {on && (
        <Card>
          <CardBody className="space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-emphasis">Recovery codes</div>
              <Badge tone={state && state.recovery_codes_left <= 3 ? "warn" : "neutral"}>
                {state ? `${state.recovery_codes_left} left` : "—"}
              </Badge>
            </div>
            <p className="text-sm text-muted-foreground">
              Each signs you in once without the app. Making a new set replaces every earlier code, and needs your
              password and the code your authenticator app shows now.
            </p>
            {codes && <RecoveryCodes codes={codes} label="New recovery codes" />}
            {!making && (
              <Button size="sm" variant="outline" onClick={() => { setMaking(true); setCodes(null); }}>Make new recovery codes</Button>
            )}
            {making && (
              <div className="flex flex-wrap items-end gap-2">
                <Field label="Your password" required hint="Confirms the change.">
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
                  />
                </Field>
                <Button size="sm" variant="primary" disabled={busy || password === "" || !codeReady} onClick={() => void makeCodes()}>
                  {busy ? "Making…" : "Make codes"}
                </Button>
                <Button size="sm" variant="ghost" disabled={busy} onClick={stopMaking}>Cancel</Button>
              </div>
            )}
          </CardBody>
        </Card>
      )}
    </PageShell>
  );
}
