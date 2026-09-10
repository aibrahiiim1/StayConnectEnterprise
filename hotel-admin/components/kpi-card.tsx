import { StatCard } from "@/components/ui/page";

/**
 * @deprecated Use `StatCard` from `@/components/ui/page`, which also carries an icon, a tone, a trend and a
 * link to the screen the figure comes from.
 *
 * Kept as a thin alias so nothing that still imports KpiCard breaks; it now renders the new tile.
 */
export function KpiCard({
  label, value, hint,
}: {
  label: string;
  value: React.ReactNode;
  hint?: React.ReactNode;
}) {
  return <StatCard label={label} value={value} hint={hint} />;
}
