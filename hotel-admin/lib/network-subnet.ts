/**
 * The size of a client network's subnet. Mirrors the appliance's rule (data-plane internal/netcfg CheckClientSubnet), which stays the
 * authority: the form only says the same thing earlier.
 *
 * The widest client network is /19. That is the ADDRESS CAPACITY of one network (8,190 usable addresses); it is not a promise that 8,190
 * devices are served at once. A site that needs more splits it into several client networks.
 */
export const WIDEST_PREFIX = 19;
export const NARROWEST_PREFIX = 30;

/** Usable host addresses (network and broadcast excluded) in a prefix: /19 -> 8190, /24 -> 254. */
export function usableAddresses(prefix: number): number {
  if (!Number.isInteger(prefix) || prefix < 0 || prefix > 30) return 0;
  return 2 ** (32 - prefix) - 2;
}

/** A sentence for an unusable subnet size, or null when the subnet is acceptable (or not yet a CIDR: other checks own that). */
export function subnetSizeProblem(cidr: string): string | null {
  const m = /^\s*\d{1,3}(?:\.\d{1,3}){3}\/(\d{1,2})\s*$/.exec(cidr);
  if (!m) return null;
  const prefix = Number(m[1]);
  if (prefix < WIDEST_PREFIX) {
    return `A client network can be at most /${WIDEST_PREFIX} (${usableAddresses(WIDEST_PREFIX).toLocaleString("en-US")} addresses). ` +
      "Use a narrower subnet, or split the site into several client networks.";
  }
  if (prefix > NARROWEST_PREFIX) return `A client network needs at least /${NARROWEST_PREFIX} (a gateway and one device).`;
  return null;
}

export const SUBNET_SIZE_HINT =
  `From /${WIDEST_PREFIX} (${usableAddresses(WIDEST_PREFIX).toLocaleString("en-US")} addresses) down to /${NARROWEST_PREFIX}. ` +
  "This is the address range of the network, not the number of devices it can serve at once.";
