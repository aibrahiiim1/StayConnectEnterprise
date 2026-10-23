"use client";

import { ThemeProvider as NextThemeProvider } from "next-themes";

// The server cannot know the stored theme; next-themes sets its class before paint, so hydration ignores it.
export function ThemeProvider({ children }: { children: React.ReactNode }) {
  return (
    <NextThemeProvider
      attribute="class"
      defaultTheme="system"
      enableSystem
      disableTransitionOnChange
      storageKey="stayconnect-admin-theme"
    >
      {children}
    </NextThemeProvider>
  );
}
