import { cn } from "@/lib/utils";

// TABLES ARE THE PRODUCT. Almost every screen here is a list of operational rows, so the table treatment is
// what the admin mostly looks like.
//
// Three things changed and all three were legibility, not decoration:
//   * the header row now has its own surface, so a long list keeps a visible top edge when scrolled;
//   * rows have a hover state, because an operator tracking one room across eight columns needs to be able to
//     see which row their eye is on;
//   * the horizontal scroll container is bounded with a rounded edge instead of bleeding off the card.
//
// Signatures are unchanged: Table/THead/TR/TH/TD are drop-in.

export function Table({ className, ...p }: React.HTMLAttributes<HTMLTableElement>) {
  return (
    <div className="w-full overflow-x-auto">
      <table className={cn("w-full border-collapse text-sm", className)} {...p} />
    </div>
  );
}

export function THead({ className, ...p }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return (
    <thead
      className={cn(
        "bg-surface/70 text-left text-2xs font-semibold uppercase tracking-wider text-muted-foreground",
        "[&_th]:border-b [&_th]:border-border",
        className,
      )}
      {...p}
    />
  );
}

export function TBody({ className, ...p }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={cn("divide-y divide-border", className)} {...p} />;
}

export function TR({ className, ...p }: React.HTMLAttributes<HTMLTableRowElement>) {
  return (
    <tr
      className={cn(
        "border-b border-border last:border-0 transition-colors",
        // Only body rows highlight. `tbody &` keeps the header row out of it without every call site having to
        // pass a different component.
        "[tbody_&]:hover:bg-surface/60",
        className,
      )}
      {...p}
    />
  );
}

export function TH({ className, ...p }: React.ThHTMLAttributes<HTMLTableCellElement>) {
  return <th className={cn("whitespace-nowrap px-4 py-2.5 font-semibold", className)} {...p} />;
}

export function TD({ className, ...p }: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn("px-4 py-3 align-middle", className)} {...p} />;
}

// TableWrap — a table that fills a card body edge to edge. Pages do this with `<CardBody className="p-0">`;
// this just names the intent and adds the rounded bottom corners the raw version loses.
export function TableWrap({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("overflow-hidden rounded-b-lg", className)} {...p} />;
}
