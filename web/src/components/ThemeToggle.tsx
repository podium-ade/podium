import { useEffect, useState } from "react";
import { Moon, Sun } from "lucide-react";
import { cn } from "@/lib/utils";
import { readTheme, syncTheme, toggleTheme, watchSystemTheme, type Theme } from "../lib/theme";

const iconButton =
  "grid size-11 shrink-0 place-items-center rounded-md border border-border bg-panel text-fg hover:bg-raised";

/**
 * ThemeToggle switches between the light and dark theme. It sits in the top bar on a wide
 * window and in the navigation drawer on a phone, where the top bar has no room to spare.
 */
export function ThemeToggle({ className }: { className?: string }) {
  const [theme, setTheme] = useState<Theme>(() =>
    typeof document === "undefined" ? "light" : readTheme(),
  );
  useEffect(() => {
    syncTheme();
    return watchSystemTheme(setTheme);
  }, []);
  const dark = theme === "dark";
  return (
    <button
      type="button"
      aria-pressed={dark}
      aria-label={dark ? "Switch to light theme" : "Switch to dark theme"}
      onClick={() => setTheme(toggleTheme())}
      className={cn(iconButton, className)}
    >
      {dark ? <Sun className="size-[18px] stroke-[1.8]" /> : <Moon className="size-[18px] stroke-[1.8]" />}
    </button>
  );
}
