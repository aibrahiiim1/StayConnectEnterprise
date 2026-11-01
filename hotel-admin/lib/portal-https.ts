// Shapes edged returns for /edge/v1/portal-https and the words they are shown with; unknown codes are shown verbatim.

export type PortalDNSRecord = { type: string; name: string; value: string };

export type PortalTLSState = {
  central_available: boolean;
  onegate_hostname: string;
  dns_state: string;
  custom_domain: string;
  custom_domain_status: string;
  custom_domain_records: PortalDNSRecord[];
  custom_domain_error: string;
  canonical_hostname: string;
  https_active: boolean;
  fallback_reason: string;
  fallback_since: string | null;
  last_sync_at: string | null;
  last_sync_error: string;
  updated_at: string | null;
  /** The shared Client Portal name while this appliance uses it; "" otherwise. */
  shared_hostname?: string;
};

export type PortalCertificate = {
  hostname: string;
  kind: "ONEGATE" | "CUSTOM" | string;
  serial: string;
  not_before: string | null;
  not_after: string | null;
  renew_after: string | null;
  fingerprint: string;
  installed_at: string | null;
  pending_order_id: string | null;
  pending_since: string | null;
  last_error: string;
  last_attempt_at: string | null;
  updated_at: string;
};

export type PortalCallbackURL = { hostname: string; kind: "ONEGATE" | "CUSTOM" | string; url: string; canonical: boolean };

export type PortalSettingsChange = {
  id: number;
  changed_at: string;
  changed_by: string;
  field: "portal_ip" | "custom_domain" | string;
  old_value: string | null;
  new_value: string | null;
  source_ip: string | null;
};

export type PortalHealth = { name: string; level: "ok" | "warn" | "critical" | string; reason?: string; message: string };

export type PortalHTTPSDoc = {
  settings: { portal_ip: string; custom_domain: string | null; updated_at: string; updated_by: string; configured: boolean };
  state: PortalTLSState;
  certificates: PortalCertificate[];
  callback_urls: PortalCallbackURL[];
  history: PortalSettingsChange[];
  health: PortalHealth;
};

export const FALLBACK_WORDS: Record<string, string> = {
  NOT_CONFIGURED: "OneGate Central has not been set up to issue portal certificates yet.",
  NO_HOSTNAME: "This appliance has not been given its portal hostname yet.",
  NO_CERTIFICATE: "No valid certificate is installed for the portal hostname yet.",
  CERTIFICATE_EXPIRED: "The portal certificate has expired.",
  PORTAL_ADDRESS_NOT_ACTIVE: "The portal address is not active on the appliance. Apply the network configuration.",
  CENTRAL_UNREACHABLE: "OneGate Central cannot be reached to obtain a certificate.",
};

export function fallbackWords(reason: string | null | undefined): string {
  if (!reason) return "The reason was not recorded.";
  return FALLBACK_WORDS[reason] ?? reason.replace(/_/g, " ").toLowerCase();
}

export const DOMAIN_STATUS: Record<string, { label: string; tone: "ok" | "warn" | "err" | "default" | "info" }> = {
  PENDING: { label: "Waiting for DNS", tone: "warn" },
  VERIFIED: { label: "Verified", tone: "ok" },
  FAILED: { label: "Failed", tone: "err" },
  REMOVED: { label: "Removed", tone: "default" },
};

export function domainStatus(status: string | null | undefined) {
  if (!status) return { label: "Not registered yet", tone: "default" as const };
  return DOMAIN_STATUS[status] ?? { label: status.toLowerCase(), tone: "default" as const };
}

export const DNS_STATE_WORDS: Record<string, string> = {
  UNKNOWN: "Not known yet",
  PENDING: "Publishing",
  SYNCED: "Published",
  ERROR: "Error",
};

export function portalHeadline(doc: Pick<PortalHTTPSDoc, "state" | "health">): {
  tone: "success" | "warning" | "danger";
  title: string;
} {
  if (doc.state.https_active) {
    return { tone: "success", title: `HTTPS is active — clients reach the portal at ${doc.state.canonical_hostname}` };
  }
  if (doc.health?.level === "critical") {
    return { tone: "danger", title: "The Client Portal has fallen back to HTTP" };
  }
  return { tone: "warning", title: "The Client Portal is on the HTTP fallback" };
}

export function looksLikePortalDomain(raw: string): string | null {
  const v = raw.trim().toLowerCase().replace(/\.$/, "");
  if (!v) return "Enter a domain such as wifi.example.com.";
  if (/^https?:\/\//.test(v) || v.includes("/")) return "Enter only the host name, without https:// or a path.";
  if (v === "onegate.getsemantics.com" || v.endsWith(".onegate.getsemantics.com")) return "Use a domain you own, not one under onegate.getsemantics.com.";
  if (!/^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(v)) {
    return "Use letters, digits, hyphens and dots only, for example wifi.example.com.";
  }
  if (v.split(".").length < 3) return `Use a subdomain such as wifi.${v}, not the domain itself.`;
  return null;
}

export const PROVIDER_LABELS: Record<string, string> = {
  google: "Google",
  apple: "Apple",
  facebook: "Facebook",
  microsoft: "Microsoft",
};

export function offeredWords(reason: string | null | undefined, provider: string): string {
  switch (reason) {
    case undefined:
    case null:
    case "":
      return "Offered to clients on the sign-in page.";
    case "disabled":
      return "Switched off: clients do not see it.";
    case "http_fallback":
      return "Not offered: the portal is on the HTTP fallback, and providers accept HTTPS callbacks only.";
    case "needs_hotel_domain":
      return `Not offered: ${PROVIDER_LABELS[provider] ?? provider} needs your portal domain (see Portal HTTPS).`;
    case "callback_not_registered":
      return "Not offered: the callback URL of the portal's current address is not ticked as registered.";
    case "method_off":
      return `Not offered: ${PROVIDER_LABELS[provider] ?? provider} is switched off under Sign-in methods.`;
    case "method_unknown":
      return "Not offered: the Sign-in methods setting could not be read.";
    default:
      return `Not offered (${reason}).`;
  }
}

export const PROVIDER_CONSOLE_HELP: Record<string, string> = {
  google:
    "Google Cloud console: verify your portal domain in Google Search Console, add that domain as an authorized domain on the OAuth consent screen, and add the callback URL of the portal domain as an authorized redirect URI. Google does not accept the OneGate hostname.",
  apple:
    "Apple Developer: create a Services ID with Sign in with Apple, add the portal hostname under Domains and Subdomains and the callback URL under Return URLs, and create a Sign in with Apple key (.p8).",
  microsoft:
    "Microsoft Entra admin center: in the app registration, add a Web platform redirect URI with the callback URL, and create a client secret.",
  facebook:
    "Meta for Developers: in Facebook Login settings, keep Strict Mode on and add the callback URL under Valid OAuth Redirect URIs.",
};

export function keepAvailable(hosts: string[], available: Pick<PortalCallbackURL, "hostname">[]): string[] {
  const known = new Set(available.map((c) => c.hostname));
  return hosts.filter((h) => known.has(h));
}
