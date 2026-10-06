import { useLayoutEffect, useRef, useState } from "react";
import type { LucideIcon } from "lucide-react";
import { ChevronDown } from "lucide-react";
import { Button } from "../ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../ui/dropdown-menu";
import { Tabs, TabsList, TabsTrigger } from "../ui/tabs";
import { sectionsOverflow } from "./sectionsOverflow";

export type SettingsSection = {
  id: string;
  label: string;
  icon: LucideIcon;
};

/**
 * SettingsSectionNav is tabs while the names fit on one row, and one menu
 * labeled with the open section once they do not. The open section stays
 * underneath; this control only changes which address is current.
 */
export function SettingsSectionNav({
  sections,
  current,
  onChange,
  collapsed,
}: {
  sections: SettingsSection[];
  current: string;
  onChange: (id: string) => void;
  /** collapsed forces the menu. Omit it and the row measures itself. */
  collapsed?: boolean;
}) {
  const frame = useRef<HTMLDivElement>(null);
  const measure = useRef<HTMLDivElement>(null);
  const [measured, setMeasured] = useState(false);
  const menu = collapsed ?? measured;
  const active = sections.find((section) => section.id === current) ?? sections[0];

  useLayoutEffect(() => {
    if (collapsed !== undefined) return;
    const frameEl = frame.current;
    const measureEl = measure.current;
    if (!frameEl || !measureEl) return;
    const check = () => {
      setMeasured(sectionsOverflow(measureEl.scrollWidth, frameEl.clientWidth));
    };
    check();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(check);
    observer.observe(frameEl);
    return () => observer.disconnect();
  }, [collapsed, sections]);

  return (
    <div ref={frame} className="relative max-w-full">
      <div
        ref={measure}
        aria-hidden
        className="pointer-events-none invisible absolute inset-x-0 top-0 flex w-full flex-nowrap gap-0.5 overflow-hidden p-0.5"
      >
        {sections.map((section) => (
          <span
            key={section.id}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 px-3 text-xs font-medium whitespace-nowrap"
          >
            <section.icon className="size-3.5" />
            {section.label}
          </span>
        ))}
      </div>

      {menu && active ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              variant="outline"
              size="sm"
              data-testid="settings-section-menu"
              className="gap-1.5"
            >
              <active.icon />
              {active.label}
              <ChevronDown className="text-muted" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="min-w-44">
            {sections.map((section) => (
              <DropdownMenuItem key={section.id} onSelect={() => onChange(section.id)}>
                <section.icon />
                {section.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      ) : (
        <Tabs value={current} onValueChange={onChange}>
          <TabsList aria-label="Settings" className="max-w-full flex-nowrap">
            {sections.map((section) => (
              <TabsTrigger key={section.id} value={section.id}>
                <section.icon />
                {section.label}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      )}
    </div>
  );
}
