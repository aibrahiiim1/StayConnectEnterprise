import "./globals.css";
import type { Metadata } from "next";
import { ThemeProvider } from "@/components/theme-provider";
import { TooltipProvider } from "@/components/ui/tooltip";

export const metadata: Metadata = {
  title: "StayConnect Hotel Admin",
  description: "On-appliance hotel management",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // suppressHydrationWarning is required by the theme script: next-themes stamps the stored choice onto <html>
    // before React hydrates, so the server-rendered markup and the first client render legitimately differ on
    // this one attribute. Without it React logs a mismatch for something that is working correctly.
    <html lang="en" suppressHydrationWarning>
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
