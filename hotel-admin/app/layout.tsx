import "./globals.css";
import type { Metadata } from "next";
import { ThemeProvider } from "@/components/theme-provider";
import { TooltipProvider } from "@/components/ui/tooltip";
import { SIDEBAR_INIT_SCRIPT } from "@/lib/sidebar-state";

export const metadata: Metadata = {
  title: "OneGate Admin Console",
  description: "On-appliance Wi-Fi management",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // suppressHydrationWarning: next-themes stamps the theme onto <html> before hydration.
    <html lang="en" suppressHydrationWarning>
      <head>
        {/* Sets sidebar width before paint; a state-driven width would snap on hydration (lib/sidebar-state.ts). */}
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
