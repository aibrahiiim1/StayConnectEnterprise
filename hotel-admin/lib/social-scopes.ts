// What a social provider is asked for: a hotel may request more than name and email in Scopes (it is the controller),
// so this warns rather than refuses.

export const DEFAULT_SCOPES: Record<string, string> = {
  google: "openid email",
  facebook: "email",
  apple: "email",
  microsoft: "openid email profile",
};

const BASIC_SCOPES: Record<string, string[]> = {
  google: ["openid", "email", "profile"],
  facebook: ["email", "public_profile"],
  apple: ["email", "name"],
  microsoft: ["openid", "email", "profile", "offline_access"],
};

export function extraScopes(provider: string, scopes: string): string[] {
  const basic = new Set(BASIC_SCOPES[provider] ?? []);
  const seen = new Set<string>();
  return scopes
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s && !basic.has(s.toLowerCase()) && !seen.has(s) && seen.add(s));
}
