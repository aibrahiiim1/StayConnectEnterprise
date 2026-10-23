// Guest portal designer API. /portal-branding/validate returns the server's verdict so the preview renders the SAFE
// version of a fragment.

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

/**
 * The portal's layouts in designer order; ids are the server's closed list (internal/portaldesign.Templates), checked
 * by a Go test.
 */
export const PORTAL_TEMPLATES: PortalTemplate[] = [
  { id: "classic", name: "Classic", description: "A centred card over your photograph. The original layout.",
    options: ["density", "heading_font"] },
  { id: "split", name: "Split", description: "Your photograph and welcome on one side, sign-in on the other. Stacks on a phone.",
    options: ["hero", "overlay", "panel_position", "density", "heading_font"] },
  { id: "immersive", name: "Immersive", description: "A full-screen photograph, large type and a frosted-glass sign-in panel.",
    options: ["hero", "overlay", "panel_position", "surface", "density", "heading_font"] },
  { id: "headerbar", name: "Header bar", description: "A business layout: top bar with your logo, sign-in beside a help column, terms in a footer.",
    options: ["density", "heading_font"] },
  { id: "editorial", name: "Editorial", description: "A tall banner with your welcome as the headline, the sign-in card overlapping it, your content below.",
    options: ["hero", "overlay", "hero_height", "density", "heading_font"] },
  { id: "kiosk", name: "Kiosk", description: "No imagery and large controls, for a shared tablet or a client in a hurry.",
    options: ["density"] },
];

export const templateById = (id?: string) => PORTAL_TEMPLATES.find((t) => t.id === id) ?? PORTAL_TEMPLATES[0];

/** Limits the server enforces (internal/portaldesign), shown as budgets rather than discovered as refusals. */
export const LIMITS = {
  advancedBytes: 64 * 1024,
  hotelName: 120,
  welcomeText: 280,
  helpText: 600,
  privacyText: 8000,
} as const;
