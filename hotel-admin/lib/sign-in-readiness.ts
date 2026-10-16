// Whether a code method's sender can deliver a code (Sign-in methods).

export type NotifyProvider = { channel: string; kind: string; enabled: boolean };

// A TEST-ONLY ("stub") sender delivers nothing to a client. In a production build of the console it does not make
// a code method ready, so the screen never shows a method as available when no code can reach anyone. Development
// and test builds still count it: it is how the flow is exercised without a real provider.
export function senderReady(providers: NotifyProvider[], channel: string,
  production: boolean = process.env.NODE_ENV === "production"): boolean {
  return providers.some((p) => p.channel === channel && p.enabled && !(production && p.kind === "stub"));
}
