"use client";

import { createContext, useContext, useEffect, useState } from "react";
import { api, Whoami } from "@/lib/api";

// THE SIGNED-IN OPERATOR, ONCE.
//
// The app layout already asks /auth/whoami before it renders anything, and it re-validates every 30 seconds.
// Pages that gate their controls on the role were asking again on every mount. They read it from here instead.
//
// Outside the layout (component tests render pages bare) there is no provider, and the hook falls back to
// fetching for itself -- so a page never depends on an ancestor it does not name.

const WhoamiContext = createContext<Whoami | null>(null);

export const WhoamiProvider = WhoamiContext.Provider;

/** The operator's roles, or null while unknown. Callers fail closed on null. */
export function useOperatorRoles(): string[] | null {
  const me = useContext(WhoamiContext);
  const [fetched, setFetched] = useState<string[] | null>(null);

  useEffect(() => {
    if (me) return;
    let alive = true;
    api
      .get<Whoami>("/auth/whoami")
      .then((m) => alive && setFetched(m.roles ?? []))
      .catch(() => alive && setFetched([]));
    return () => {
      alive = false;
    };
  }, [me]);

  return me ? me.roles ?? [] : fetched;
}
