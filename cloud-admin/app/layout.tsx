import "./globals.css";
import type { Metadata } from "next";
import { ThemeProvider } from "@/components/theme-provider";
import { TooltipProvider } from "@/components/ui/tooltip";
import { SIDEBAR_INIT_SCRIPT } from "@/lib/sidebar-state";

export const metadata: Metadata = {
  title: "Velonet Central",
  description: "Velonet vendor console: customers, sites, appliance activation and licenses",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // data-product="central" swaps only the accent (teal) so the vendor console can never be mistaken for a
    // hotel's appliance; every other token is shared with Hotel Admin (design-system/README.md).
    // suppressHydrationWarning: next-themes stamps the stored theme onto <html> before React hydrates.
    <html lang="en" dir="ltr" data-product="central" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: SIDEBAR_INIT_SCRIPT }} />
      </head>
      <body className="min-h-screen bg-background font-sans text-foreground antialiased">
        <ThemeProvider>
          <TooltipProvider delayDuration={150} skipDelayDuration={300}>
            {children}
          </TooltipProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}
