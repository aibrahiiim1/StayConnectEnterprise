// What a social provider is asked for (GDPR remediation plan C9, decision D-C9).
//
// A guest sign-in keeps only the provider's subject id and the verified email. A hotel may still ask a provider
// for more in the Scopes field: the hotel is the controller and decides, so this warns rather than refuses.
// "More" is anything beyond the scopes for the guest's name and email; asking for the name alone is not
// warned about.

/** What the appliance asks for when Scopes is left empty (internal/social, default*Scope). */
export const DEFAULT_SCOPES: Record<string, string> = {
  google: "openid email",
  facebook: "email",
  apple: "email",
  microsoft: "openid email profile",
};

/** Scopes that ask for no more than the guest's name and email. */
const BASIC_SCOPES: Record<string, string[]> = {
  google: ["openid", "email", "profile"],
  facebook: ["email", "public_profile"],
  apple: ["email", "name"],
  microsoft: ["openid", "email", "profile", "offline_access"],
};

/** The scopes in `scopes` that ask the provider for more than the guest's name and email. */
export function extraScopes(provider: string, scopes: string): string[] {
  const basic = new Set(BASIC_SCOPES[provider] ?? []);
  const seen = new Set<string>();
  return scopes
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s && !basic.has(s.toLowerCase()) && !seen.has(s) && seen.add(s));
}
