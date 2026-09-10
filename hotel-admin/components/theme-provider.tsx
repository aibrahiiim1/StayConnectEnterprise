"use client";

import { ThemeProvider as NextThemeProvider } from "next-themes";

// THE THEME IS THE OPERATOR'S CHOICE, AND IT IS REMEMBERED.
//
// The admin was dark because the stylesheet had no other option, not because anybody picked it. Three states
// are offered instead: light, dark, and follow the operating system — with "system" as the default, so an
// operator who has already told their OS which they prefer does not have to tell this product as well.
//
// `attribute="class"` is what Tailwind's `darkMode: "class"` reads. next-themes writes the class before first
// paint via a tiny inline script, which is why <html> carries suppressHydrationWarning in the root layout:
// the server cannot know the stored choice, and the client correcting it is expected rather than a mismatch.
//
// disableTransitionOnChange stops every colour on the page animating at once when the theme flips, which
// looks like a rendering fault rather than a deliberate switch.
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
