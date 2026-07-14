import { cn } from "@/lib/utils";

export function Table({ className, ...p }: React.HTMLAttributes<HTMLTableElement>) {
  return (
    <div className="overflow-x-auto">
      <table className={cn("w-full text-sm", className)} {...p} />
    </div>
  );
}
export function THead({ className, ...p }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <thead className={cn("text-left text-muted text-xs uppercase tracking-wider", className)} {...p} />;
}
export function TR({ className, ...p }: React.HTMLAttributes<HTMLTableRowElement>) {
  return <tr className={cn("border-b border-border last:border-0", className)} {...p} />;
}
export function TH({ className, ...p }: React.ThHTMLAttributes<HTMLTableCellElement>) {
  return <th className={cn("px-4 py-3 font-medium", className)} {...p} />;
}
export function TD({ className, ...p }: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn("px-4 py-3 align-middle", className)} {...p} />;
}
