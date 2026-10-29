export const THEME_KEY = "podium-relay-theme";

export type Theme = "light" | "dark";

const THEME_COLOR: Record<Theme, string> = {
  light: "#f2f4f6",
  dark: "#060708",
};

/** A theme the user picked with the toggle. Nothing stored means follow the system. */
export function storedTheme(): Theme | null {
  try {
    const stored = localStorage.getItem(THEME_KEY);
    if (stored === "dark" || stored === "light") return stored;
  } catch {
    // Private windows can refuse storage. Fall through to the system theme.
  }
  return null;
}

export function systemTheme(): Theme {
  try {
    if (window.matchMedia("(prefers-color-scheme: dark)").matches) return "dark";
  } catch {
    // jsdom and locked-down browsers have no matchMedia.
  }
  return "light";
}

export function readTheme(): Theme {
  return storedTheme() ?? systemTheme();
}

function paintTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", THEME_COLOR[theme]);
}

/** Paint the resolved theme without saving it, so a system change is not frozen as a choice. */
export function syncTheme(): Theme {
  const theme = readTheme();
  paintTheme(theme);
  return theme;
}

export function applyTheme(theme: Theme) {
  paintTheme(theme);
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch {
    // A private window can refuse storage. The attribute still paints this page.
  }
}

export function toggleTheme(): Theme {
  const next: Theme = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
  applyTheme(next);
  return next;
}

/**
 * Keep the page on the OS theme until the user picks one. A saved choice wins, and this
 * listener then does nothing.
 */
export function watchSystemTheme(onChange: (theme: Theme) => void): () => void {
  if (storedTheme()) return () => {};
  let media: MediaQueryList;
  try {
    media = window.matchMedia("(prefers-color-scheme: dark)");
  } catch {
    return () => {};
  }
  if (typeof media.addEventListener !== "function") return () => {};
  const onMedia = () => {
    if (storedTheme()) return;
    const theme: Theme = media.matches ? "dark" : "light";
    paintTheme(theme);
    onChange(theme);
  };
  media.addEventListener("change", onMedia);
  return () => media.removeEventListener("change", onMedia);
}
