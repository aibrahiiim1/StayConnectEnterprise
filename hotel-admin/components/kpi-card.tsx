import { Card, CardBody } from "@/components/ui/card";

export function KpiCard({
  label, value, hint,
}: {
  label: string;
  value: React.ReactNode;
  hint?: React.ReactNode;
}) {
  return (
    <Card>
      <CardBody>
        <div className="text-xs text-muted uppercase tracking-wider">{label}</div>
        <div className="text-2xl font-semibold mt-1">{value}</div>
        {hint && <div className="text-xs text-muted mt-1">{hint}</div>}
      </CardBody>
    </Card>
  );
}
