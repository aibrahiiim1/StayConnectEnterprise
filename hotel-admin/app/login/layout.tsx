import type { Metadata } from "next";

// The sign-in page's own tab title (WCAG 2.4.2). The page is a client component and cannot export metadata, so
// it lives here; the console's screens set theirs from the menu label (app/(app)/layout.tsx).
export const metadata: Metadata = {
  title: "Sign in · OneGate Admin Console",
};

export default function LoginLayout({ children }: { children: React.ReactNode }) {
  return children;
}
