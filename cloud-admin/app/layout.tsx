import "./globals.css";
import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "StayConnect Admin",
  description: "Gateway appliance management",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen antialiased font-sans">{children}</body>
    </html>
  );
}
