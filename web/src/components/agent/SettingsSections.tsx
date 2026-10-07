import type { LucideIcon } from "lucide-react";
import { Link } from "react-router";
import { cn } from "@/lib/utils";

export type SettingsSection = {
  id: string;
  label: string;
  icon: LucideIcon;
};

/**
 * SettingsSectionNav is the list of sections beside the open one. Each name is
 * the address of that section, so back and a deep link land on it. The list
 * stays a column when the window is narrow, above the section it opens.
 */
export function SettingsSectionNav({
  sections,
  current,
}: {
  sections: SettingsSection[];
  current: string;
}) {
  return (
    <nav
      aria-label="Settings"
      className="border-b border-border pb-3 sm:sticky sm:top-0 sm:w-44 sm:shrink-0 sm:self-start sm:border-r sm:border-b-0 sm:pr-6 sm:pb-0"
    >
      <ul className="flex flex-col gap-0.5">
        {sections.map((section) => {
          const open = section.id === current;
          return (
            <li key={section.id}>
              <Link
                to={`/agent/settings/${section.id}`}
                aria-current={open ? "page" : undefined}
                className={cn(
                  "flex h-8 items-center gap-2 rounded-md px-2.5 text-sm transition-colors duration-150",
                  "outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  open ? "bg-raised font-medium text-fg shadow-xs" : "text-muted hover:bg-panel hover:text-fg",
                )}
              >
                <section.icon className="size-4 shrink-0" />
                {section.label}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
