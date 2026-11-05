// The WAN / LAN page's local pending lock.

/** After a read that found no pending change: a pending apply whose deadline has passed is over, so its lock goes; one still
 * inside its window (the read may predate the apply reaching the appliance) is kept. */
export function clearExpiredLock<T extends { state?: string; deadline_unix?: number }>(resp: T | null, now: number): T | null {
  if (resp?.state === "pending_confirmation" && (resp.deadline_unix ?? 0) * 1000 <= now) return null;
  return resp;
}
