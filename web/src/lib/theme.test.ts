import { afterEach, describe, expect, it, vi } from "vitest";
import { THEME_KEY, readTheme, syncTheme, toggleTheme, watchSystemTheme } from "./theme";

type Listener = (event: { matches: boolean }) => void;

function installMedia(dark: boolean) {
  const listeners = new Set<Listener>();
  const media = {
    matches: dark,
    media: "(prefers-color-scheme: dark)",
    addEventListener: (_type: string, listener: Listener) => {
      listeners.add(listener);
    },
    removeEventListener: (_type: string, listener: Listener) => {
      listeners.delete(listener);
    },
    emit() {
      for (const listener of listeners) listener({ matches: media.matches });
    },
  };
  vi.stubGlobal("matchMedia", (query: string) => {
    if (query !== "(prefers-color-scheme: dark)") throw new Error(query);
    return media;
  });
  return media;
}

afterEach(() => {
  localStorage.removeItem(THEME_KEY);
  document.documentElement.removeAttribute("data-theme");
  vi.unstubAllGlobals();
});

describe("readTheme", () => {
  it("follows a dark system when nothing is saved", () => {
    installMedia(true);
    expect(readTheme()).toBe("dark");
  });

  it("follows a light system when nothing is saved", () => {
    installMedia(false);
    expect(readTheme()).toBe("light");
  });

  it("keeps an explicit choice ahead of the system", () => {
    installMedia(true);
    localStorage.setItem(THEME_KEY, "light");
    expect(readTheme()).toBe("light");
  });

  it("is light when the browser cannot report a preference", () => {
    vi.stubGlobal("matchMedia", undefined);
    expect(readTheme()).toBe("light");
  });
});

describe("syncTheme", () => {
  it("paints the system theme without saving it", () => {
    installMedia(true);
    expect(syncTheme()).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem(THEME_KEY)).toBeNull();
  });
});

describe("watchSystemTheme", () => {
  it("repaints when the system theme changes and nothing is saved", () => {
    const media = installMedia(false);
    const seen: string[] = [];
    const stop = watchSystemTheme((theme) => seen.push(theme));
    media.matches = true;
    media.emit();
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(seen).toEqual(["dark"]);
    stop();
    media.matches = false;
    media.emit();
    expect(seen).toEqual(["dark"]);
  });

  it("leaves a saved choice alone when the system changes", () => {
    const media = installMedia(true);
    localStorage.setItem(THEME_KEY, "light");
    const seen: string[] = [];
    watchSystemTheme((theme) => seen.push(theme));
    media.matches = false;
    media.emit();
    expect(seen).toEqual([]);
    expect(document.documentElement.dataset.theme).toBeUndefined();
  });
});

describe("toggleTheme", () => {
  it("saves the opposite of the painted theme", () => {
    installMedia(true);
    document.documentElement.dataset.theme = "dark";
    expect(toggleTheme()).toBe("light");
    expect(localStorage.getItem(THEME_KEY)).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");
  });
});
