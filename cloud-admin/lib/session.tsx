"use client";

// The server scopes customer users; periodic whoami refreshes apply role changes without reloading.

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
