"use client";

// Route shell for the Phase-4 (DARK) settlement browser. Read-only: there is no authorized operator write
// on this surface in Phase 4, so it needs no permission beyond reaching the page.

import { SettlementsView } from "@/components/phase4/settlements-view";

export default function FinancialSettlementsPage() {
  return <SettlementsView />;
}
