"use client";

import * as React from "react";
import * as MenuPrimitive from "@radix-ui/react-dropdown-menu";
import { Check } from "lucide-react";
import { cn } from "@/lib/utils";

export const Menu = MenuPrimitive.Root;
export const MenuTrigger = MenuPrimitive.Trigger;
export const MenuGroup = MenuPrimitive.Group;

export function MenuContent({
  className,
  align = "end",
  sideOffset = 4,
  ...p
}: React.ComponentPropsWithoutRef<typeof MenuPrimitive.Content>) {
  return (
    <MenuPrimitive.Portal>
      <MenuPrimitive.Content
        align={align}
        sideOffset={sideOffset}
        collisionPadding={8}
        className={cn(
          "z-[60] min-w-44 overflow-hidden rounded-lg border border-border bg-popover p-1 text-popover-foreground shadow-overlay",
          "data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0",
          "data-[state=open]:zoom-in-95 data-[side=bottom]:slide-in-from-top-1 data-[side=top]:slide-in-from-bottom-1",
          "motion-reduce:animate-none",
          className,
        )}
        {...p}
      />
    </MenuPrimitive.Portal>
  );
}

export function MenuItem({
  className,
  danger,
  icon,
  children,
  ...p
}: React.ComponentPropsWithoutRef<typeof MenuPrimitive.Item> & { danger?: boolean; icon?: React.ReactNode }) {
  return (
    <MenuPrimitive.Item
      className={cn(
        "relative flex cursor-pointer select-none items-center gap-2 rounded-md px-2 py-1.5 text-sm outline-none pointer-coarse:min-h-11",
        "data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        danger
          ? "text-destructive data-[highlighted]:bg-destructive-subtle data-[highlighted]:text-destructive-subtle-foreground"
          : "text-foreground data-[highlighted]:bg-accent",
        "[&_svg]:size-4 [&_svg]:shrink-0",
        className,
      )}
      {...p}
    >
      {p.asChild ? children : (
        <>
          {icon && <span className={cn("inline-flex", danger ? "text-current" : "text-muted-foreground")} aria-hidden>{icon}</span>}
          {children}
        </>
      )}
    </MenuPrimitive.Item>
  );
}

export function MenuCheckboxItem({
  className,
  children,
  ...p
}: React.ComponentPropsWithoutRef<typeof MenuPrimitive.CheckboxItem>) {
  return (
    <MenuPrimitive.CheckboxItem
      className={cn(
        "relative flex cursor-pointer select-none items-center gap-2 rounded-md py-1.5 pe-2 ps-7 text-sm outline-none",
        "data-[highlighted]:bg-accent data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        className,
      )}
      {...p}
    >
      <span className="absolute start-2 inline-flex size-4 items-center justify-center">
        <MenuPrimitive.ItemIndicator>
          <Check className="size-3.5" aria-hidden />
        </MenuPrimitive.ItemIndicator>
      </span>
      {children}
    </MenuPrimitive.CheckboxItem>
  );
}

export function MenuLabel({ className, ...p }: React.ComponentPropsWithoutRef<typeof MenuPrimitive.Label>) {
  return <MenuPrimitive.Label className={cn("px-2 py-1.5 text-2xs font-medium text-muted-foreground", className)} {...p} />;
}

export function MenuSeparator({ className, ...p }: React.ComponentPropsWithoutRef<typeof MenuPrimitive.Separator>) {
  return <MenuPrimitive.Separator className={cn("-mx-1 my-1 h-px bg-border", className)} {...p} />;
}
