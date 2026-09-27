"use client";

import { createContext, useContext, useEffect, useRef, useState } from "react";
import { api, Whoami } from "@/lib/api";
import { usePoll } from "@/lib/use-poll";

// THE SIGNED-IN OPERATOR, ONCE.
//
// The app layout asks /auth/whoami before it renders anything, and re-validates every 30 seconds. Pages that
// gate their controls on the role read it from here instead of asking again on every mount.
//
// Outside the layout (component tests render pages bare) there is no provider, and the hook falls back to
// fetching for itself -- so a page never depends on an ancestor it does not name.

const WhoamiContext = createContext<Whoami | null>(null);

export const WhoamiProvider = WhoamiContext.Provider;

/** How often the layout re-validates the session. */
export const WHOAMI_REVALIDATE_MS = 30_000;

/**
 * The layout's session: the first answer, then a re-validation every 30 seconds (paused in a hidden tab).
 *
 * A failure calls `onInvalid` (the layout bounces to /login). A success REPLACES the identity rather than being
 * discarded: another administrator may have added or removed this operator's role while the console was open,
 * and the nav and every role-gated page read it from here -- a revoked control must disappear, a granted one
 * appear, without a reload. An answer identical to the current one keeps the same object, so a quiet poll
 * re-renders nothing.
 */
export function useWhoamiSession(onInvalid: () => void | Promise<void>): { me: Whoami | null; loading: boolean } {
  const [me, setMe] = useState<Whoami | null>(null);
  const [loading, setLoading] = useState(true);
  const invalid = useRef(onInvalid);
  useEffect(() => {
    invalid.current = onInvalid;
  }, [onInvalid]);

  const accept = (m: Whoami) => setMe((prev) => (prev && JSON.stringify(prev) === JSON.stringify(m) ? prev : m));

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const m = await api.get<Whoami>("/auth/whoami");
        if (!cancelled) accept(m);
      } catch {
        if (!cancelled) await invalid.current();
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  usePoll(
    () => {
      api
        .get<Whoami>("/auth/whoami")
        .then(accept)
        .catch(() => invalid.current());
    },
    WHOAMI_REVALIDATE_MS,
    { enabled: me !== null },
  );

  return { me, loading };
}

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
