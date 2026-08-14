"use client";

// Route shell for the Phase-4 (DARK) financial health screen. Read-only: there is no action on this page,
// so it needs no permission beyond reaching it, and edged enforces the real gate on every request.

import { FinancialHealthView } from "@/components/phase4/financial-health-view";

export default function FinancialHealthPage() {
  return <FinancialHealthView />;
}
