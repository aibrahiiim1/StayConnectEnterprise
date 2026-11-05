"use client";

import * as React from "react";
import Link from "next/link";
import { MoreHorizontal } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from "./menu";
import { Tooltip } from "./tooltip";

export type RowAction = {
  label: string;
  onSelect?: () => void;
  /** Navigates instead of acting. */
  href?: string;
  icon?: React.ReactNode;
  /** Destructive actions are drawn in red and listed last, after a separator. */
  danger?: boolean;
  disabled?: boolean;
  reason?: string;
  /** Omits the action; lets callers list every action and gate each one by role inline. */
  hidden?: boolean;
};

/**
 * The actions of one table row: at most one inline (the most frequent, never destructive), the rest in a "⋯" menu.
 * `subject` names the row for screen readers ("Actions for alex.morgan"). Clicks never reach a clickable row.
 */
export function RowActions({
  subject,
  primary,
  items = [],
  className,
}: {
  subject: string;
  primary?: RowAction;
  items?: RowAction[];
  className?: string;
}) {
  const visible = items.filter((a) => !a.hidden);
  const safe = visible.filter((a) => !a.danger);
  const danger = visible.filter((a) => a.danger);
  const stop = (e: React.SyntheticEvent) => e.stopPropagation();

  return (
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions, jsx-a11y/click-events-have-key-events
    <div data-row-actions="" className={cn("flex items-center justify-end gap-1", className)} onClick={stop} onKeyDown={stop}>
      {primary && !primary.hidden && (
        primary.href ? (
          <Link
            href={primary.href}
            className="inline-flex h-7 items-center rounded-md px-2 text-xs font-medium text-foreground hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring pointer-coarse:min-h-11"
            aria-label={`${primary.label}: ${subject}`}
          >
            {primary.label}
          </Link>
        ) : primary.disabled && primary.reason ? (
          <Tooltip content={primary.reason}>
            <Button
              size="xs"
              variant="ghost"
              className="cursor-not-allowed text-foreground opacity-50"
              aria-disabled="true"
              onClick={(e) => e.preventDefault()}
              aria-label={`${primary.label}: ${subject}. Not available: ${primary.reason}`}
            >
              {primary.icon}
              {primary.label}
            </Button>
          </Tooltip>
        ) : (
          <Button
            size="xs"
            variant="ghost"
            className="text-foreground"
            disabled={primary.disabled}
            onClick={primary.onSelect}
            aria-label={`${primary.label}: ${subject}`}
          >
            {primary.icon}
            {primary.label}
          </Button>
        )
      )}
      {visible.length > 0 && (
        <Menu modal={false}>
          <MenuTrigger asChild>
            <Button size="icon-xs" variant="ghost" aria-label={`Actions for ${subject}`}>
              <MoreHorizontal />
            </Button>
          </MenuTrigger>
          <MenuContent>
            {safe.map((a) => <ActionItem key={a.label} action={a} />)}
            {safe.length > 0 && danger.length > 0 && <MenuSeparator />}
            {danger.map((a) => <ActionItem key={a.label} action={a} />)}
          </MenuContent>
        </Menu>
      )}
    </div>
  );
}

function ActionItem({ action: a }: { action: RowAction }) {
  if (a.disabled && a.reason) {
    return (
      <MenuItem
        danger={a.danger}
        icon={a.icon}
        aria-disabled="true"
        className="cursor-not-allowed items-start opacity-60 data-[highlighted]:bg-transparent"
        onSelect={(e) => e.preventDefault()}
      >
        <span className="min-w-0">
          <span className="block">{a.label}</span>
          <span className="block text-2xs text-muted-foreground">{a.reason}</span>
        </span>
      </MenuItem>
    );
  }
  if (a.href) {
    return (
      <MenuItem asChild danger={a.danger} disabled={a.disabled}>
        <Link href={a.href}>
          {a.icon && <span className="inline-flex text-muted-foreground [&_svg]:size-4" aria-hidden>{a.icon}</span>}
          {a.label}
        </Link>
      </MenuItem>
    );
  }
  return (
    <MenuItem danger={a.danger} disabled={a.disabled} icon={a.icon} onSelect={() => a.onSelect?.()}>
      {a.label}
    </MenuItem>
  );
}
