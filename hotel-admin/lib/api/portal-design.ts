// THE GUEST PORTAL DESIGNER'S API.
//
// The design itself is read and saved through the routes the settings screen always used
// (/portal-branding, /portal-branding/settings, /portal-branding/draft, /portal-branding/rollback/{n}). What is
// new is /portal-branding/validate: the server's own answer to "what would the portal do with this design?",
// so the Advanced editors can name a problem while it is being typed and the preview can render the SAFE
// version of a fragment rather than the text in the box.

import { api } from "@/lib/api";

export type DesignIssue = {
  field: string;
  message: string;
  /** error: the design cannot be saved like this. warning: it saves, and the portal serves it changed. */
  severity: "error" | "warning";
};

export type ValidateResp = {
  ok: boolean;
  issues: DesignIssue[];
  /** What a guest would actually receive for the two Advanced fields. */
  sanitized: { custom_css: string; custom_html: string };
};

export function validateDesign(design: Record<string, unknown>) {
  return api.post<ValidateResp>("/portal-branding/validate", { design });
}

export type RevisionSummary = {
  version: number;
  published_at: string;
  published_by?: string;
  note?: string;
};

export type BrandingState<D> = {
  design: D;
  draft: D;
  revisions?: RevisionSummary[];
  published?: boolean;
};

/** Restoring an earlier save re-publishes it as a NEW entry in the history; it rewrites nothing. */
export function restoreRevision(version: number, password: string) {
  return api.post<{ version: number; restored_from: number }>(
    `/portal-branding/rollback/${encodeURIComponent(String(version))}`, { password });
}

export type PortalTemplate = {
  id: TemplateId;
  name: string;
  description: string;
  /** Which options this layout actually uses, so the designer only offers controls that change something. */
  options: TemplateOption[];
};

export type TemplateId = "classic" | "split" | "immersive" | "headerbar" | "editorial" | "kiosk";
export type TemplateOption = "overlay" | "panel_position" | "hero_height" | "surface" | "heading_font" | "density" | "hero";

/** The portal's layouts, in the order the designer offers them. The ids are the closed list the server accepts
 *  (internal/portaldesign.Templates); a Go test reads this file and fails if the two lists drift. */
export const PORTAL_TEMPLATES: PortalTemplate[] = [
  { id: "classic", name: "Classic", description: "A centred card over your photograph. The original layout.",
    options: ["density", "heading_font"] },
  { id: "split", name: "Split", description: "Your photograph and welcome on one side, sign-in on the other. Stacks on a phone.",
    options: ["hero", "overlay", "panel_position", "density", "heading_font"] },
  { id: "immersive", name: "Immersive", description: "A full-screen photograph, large type and a frosted-glass sign-in panel.",
    options: ["hero", "overlay", "panel_position", "surface", "density", "heading_font"] },
  { id: "headerbar", name: "Header bar", description: "A business layout: top bar with your logo, sign-in beside a help column, terms in a footer.",
    options: ["density", "heading_font"] },
  { id: "editorial", name: "Resort", description: "A tall banner with your welcome as the headline, the sign-in card overlapping it, your content below.",
    options: ["hero", "overlay", "hero_height", "density", "heading_font"] },
  { id: "kiosk", name: "Kiosk", description: "No imagery and large controls, for a lobby tablet or a guest in a hurry.",
    options: ["density"] },
];

export const templateById = (id?: string) => PORTAL_TEMPLATES.find((t) => t.id === id) ?? PORTAL_TEMPLATES[0];

/** Limits the server enforces (internal/portaldesign), shown as budgets rather than discovered as refusals. */
export const LIMITS = {
  advancedBytes: 64 * 1024,
  hotelName: 120,
  welcomeText: 280,
  helpText: 600,
} as const;
