export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="py-14 text-center text-muted">
      <div className="text-sm">{title}</div>
      {hint && <div className="text-xs mt-1">{hint}</div>}
    </div>
  );
}
