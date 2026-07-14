import { Whoami } from "@/lib/api";

// isPlatform decides which administration console the operator sees. A platform
// (StayConnect vendor) operator gets the cross-tenant Platform Console; everyone
// else gets the tenant/hotel-group portal. Backend authorization is the real
// gate — this only drives the shell/navigation.
export function isPlatform(me: Whoami | null): boolean {
  return !!me && (me.is_super_admin || (me.roles ?? []).includes("platform_admin"));
}
