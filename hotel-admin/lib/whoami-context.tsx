"use client";

import { createContext, useContext, useEffect, useRef, useState } from "react";
import { api, Whoami } from "@/lib/api";
import { usePoll } from "@/lib/use-poll";

// The signed-in operator, fetched once by the app layout and re-validated every 30 seconds. Without a provider (bare
// component tests) the hook fetches for itself.

const WhoamiContext = createContext<Whoami | null>(null);

export const WhoamiProvider = WhoamiContext.Provider;

export const WHOAMI_REVALIDATE_MS = 30_000;

/**
 * The layout's session: first answer, then re-validation (paused in a hidden tab). A failure calls `onInvalid`; a
 * success REPLACES the
 * identity so role changes made elsewhere show without a reload. An identical answer keeps the same object.
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
    () =>
      api
        .get<Whoami>("/auth/whoami")
        .then(accept)
        .catch(() => invalid.current()),
    WHOAMI_REVALIDATE_MS,
    { enabled: me !== null },
  );

  return { me, loading };
}

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

export function useOperatorEmail(): string | null {
  const me = useContext(WhoamiContext);
  const [fetched, setFetched] = useState<string | null>(null);
  useEffect(() => {
    if (me) return;
    let alive = true;
    api
      .get<Whoami>("/auth/whoami")
      .then((m) => alive && setFetched(m.email ?? null))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [me]);
  return me ? me.email ?? null : fetched;
}
