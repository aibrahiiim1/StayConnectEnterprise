"use client";

// THE SIGN-IN SCREEN — the first thing anyone sees, and the only screen outside the app shell.
//
// It has no sidebar and no top bar, so the theme control has to be here too: an operator whose appliance is set
// to dark would otherwise meet a light login page and conclude the theme preference had not stuck.

import { Suspense, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Wifi } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { Card, CardBody } from "@/components/ui/card";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ThemeToggle } from "@/components/theme-toggle";
import { api } from "@/lib/api";

export default function LoginPage() {
  // useSearchParams must live inside a Suspense boundary for production build.
  return (
    <Suspense fallback={null}>
      <LoginInner />
    </Suspense>
  );
}

function LoginInner() {
  const router = useRouter();
  const params = useSearchParams();
  const next = params.get("next") || "/dashboard";

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    setLoading(true);
    try {
      // POST /edge/v1/auth/login — local site operators only; the appliance
      // has no org concept and no SSO.
      await api.post("/auth/login", { email, password });
      router.replace(next);
      router.refresh();
    } catch (e) {
      setErr(e);
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      <div className="flex justify-end p-4">
        <ThemeToggle />
      </div>

      <main className="flex flex-1 items-center justify-center px-4 pb-20">
        <div className="w-full max-w-sm">
          <div className="mb-6 flex flex-col items-center text-center">
            <span className="mb-3 flex size-11 items-center justify-center rounded-xl bg-primary text-primary-foreground">
              <Wifi className="size-5" />
            </span>
            <h1 className="text-lg font-semibold tracking-tight">StayConnect Hotel Admin</h1>
            <p className="mt-1 text-sm text-muted-foreground">
              Sign in with your account for this property.
            </p>
          </div>

          <Card>
            <CardBody className="space-y-4">
              <ErrorBanner err={err} className="mb-0" />
              <form onSubmit={onSubmit} className="space-y-4">
                <Field label="Email or username">
                  <Input
                    type="text"
                    required
                    autoFocus
                    autoComplete="username"
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                  />
                </Field>
                <Field label="Password">
                  <Input
                    type="password"
                    required
                    autoComplete="current-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </Field>
                <Button type="submit" disabled={loading} className="w-full" size="lg">
                  {loading ? "Signing in…" : "Sign in"}
                </Button>
              </form>
            </CardBody>
          </Card>

          <p className="mt-4 text-center text-xs leading-relaxed text-muted-foreground">
            This account is managed on this appliance. It is not a StayConnect cloud account and does not work at
            any other property.
          </p>
        </div>
      </main>
    </div>
  );
}
