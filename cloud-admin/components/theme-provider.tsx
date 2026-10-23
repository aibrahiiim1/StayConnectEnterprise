"use client";

import { ThemeProvider as NextThemeProvider } from "next-themes";

// next-themes sets the class before paint; the server cannot know the stored theme.
// Disable transitions while switching to avoid animating every color at once.
export function ThemeProvider({ children }: { children: React.ReactNode }) {
  return (
    <NextThemeProvider
      attribute="class"
      defaultTheme="system"
      enableSystem
      disableTransitionOnChange
      storageKey="onegate-central-theme"
    >
      {children}
    </NextThemeProvider>
  );
}
