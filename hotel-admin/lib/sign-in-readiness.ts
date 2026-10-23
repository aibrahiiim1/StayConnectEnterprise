export type NotifyProvider = { channel: string; kind: string; enabled: boolean };

// A test-only ("stub") sender never makes a code method ready in a production console build; dev/test builds still
// count it.
export function senderReady(providers: NotifyProvider[], channel: string,
  production: boolean = process.env.NODE_ENV === "production"): boolean {
  return providers.some((p) => p.channel === channel && p.enabled && !(production && p.kind === "stub"));
}
