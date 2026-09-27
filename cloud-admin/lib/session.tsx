"use client";

// THE SIGNED-IN OPERATOR, for every screen. Built from GET /v1/auth/whoami by the app shell, which re-reads it
// every 30 seconds, so a role changed mid-session reaches the screens without a reload. There is no customer
// selector: a platform operator sees the fleet and drills into a customer; a customer user is scoped by the
// server to their own customer.

import { createContext, useContext, useMemo } from "react";
import type { Whoami } from "./api";
import { capabilitiesFor, subjectFrom, type Can, type Subject } from "./permissions";

type SessionValue = { me: Whoami; subject: Subject; can: Can };

const Ctx = createContext<SessionValue | null>(null);

export function SessionProvider({ me, children }: { me: Whoami; children: React.ReactNode }) {
  const value = useMemo(() => {
    const subject = subjectFrom(me);
    return { me, subject, can: capabilitiesFor(subject) };
  }, [me]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useSession(): SessionValue {
  const v = useContext(Ctx);
  if (!v) throw new Error("useSession must be used within SessionProvider");
  return v;
}
