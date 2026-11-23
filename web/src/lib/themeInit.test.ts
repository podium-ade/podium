import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { THEME_KEY, readTheme } from "./theme";

// vitest runs from web/, and import.meta.url is not a file URL under jsdom.
const initScript = readFileSync(resolve("public/theme-init.js"), "utf8");
const indexHtml = readFileSync(resolve("index.html"), "utf8");

// Runs the pre-paint script the way the browser does, against the jsdom document.
function runInit() {
  new Function(initScript)();
  return document.documentElement.dataset.theme;
}

function systemIs(dark: boolean) {
  vi.stubGlobal("matchMedia", () => ({ matches: dark }));
}

afterEach(() => {
  localStorage.removeItem(THEME_KEY);
  document.documentElement.removeAttribute("data-theme");
  vi.unstubAllGlobals();
});

describe("index.html theme bootstrap", () => {
  // The server answers with script-src 'self', so an inline script never runs there and the
  // sign-in screens render light whatever was chosen.
  it("loads the script from a file, not inline", () => {
    expect(indexHtml).toContain('<script src="/theme-init.js"></script>');
    expect(indexHtml).not.toMatch(/<script>/);
  });

  it("follows a dark system when nothing is saved", () => {
    systemIs(true);
    expect(runInit()).toBe("dark");
  });

  it("keeps a saved light choice on a dark system", () => {
    systemIs(true);
    localStorage.setItem(THEME_KEY, "light");
    expect(runInit()).toBe("light");
  });

  it("keeps a saved dark choice on a light system", () => {
    systemIs(false);
    localStorage.setItem(THEME_KEY, "dark");
    expect(runInit()).toBe("dark");
  });

  it("ignores a junk saved value", () => {
    systemIs(true);
    localStorage.setItem(THEME_KEY, "sepia");
    expect(runInit()).toBe("dark");
  });

  it("agrees with readTheme in every case", () => {
    for (const dark of [true, false]) {
      for (const saved of [null, "light", "dark", "sepia"]) {
        systemIs(dark);
        if (saved) localStorage.setItem(THEME_KEY, saved);
        else localStorage.removeItem(THEME_KEY);
        expect(runInit()).toBe(readTheme());
      }
    }
  });
});
