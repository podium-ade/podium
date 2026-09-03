// The argument parsing of /opt/podium-agent/bin/screenshot. It lives here, in the runtime's
// own source, so it is typechecked and unit-tested; the part that drives a browser lives in
// browser/screenshot.mjs, beside the only node_modules that has one.

/** NavigationTimeoutMs bounds the navigation. A page that is not up in 15s is not up. */
export const NavigationTimeoutMs = 15_000;

/** Usage is the one line printed for anything the parser refuses. */
export const Usage = "usage: screenshot URL OUT.png [--width N] [--height N] [--full-page]";

/** MaxPixels caps a dimension: a viewport bigger than this is a typo, not a request. */
export const MaxPixels = 10_000;

export type ScreenshotOptions = {
  url: string;
  out: string;
  width: number;
  height: number;
  fullPage: boolean;
};

/**
 * parseArgs reads the helper's argv. Every refusal names what was wrong: the caller is an
 * agent that gets one line of stderr and no second chance to ask.
 */
export function parseArgs(argv: readonly string[]): ScreenshotOptions {
  let width = 1280;
  let height = 800;
  let fullPage = false;
  const positional: string[] = [];

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i] as string;
    if (arg === "--full-page") {
      fullPage = true;
      continue;
    }
    if (arg === "--width" || arg === "--height") {
      const raw = argv[++i];
      const value = Number(raw);
      if (raw === undefined || !Number.isInteger(value) || value < 1 || value > MaxPixels) {
        throw new Error(`${arg} needs a whole number of pixels from 1 to ${MaxPixels}, got ${raw ?? "nothing"}`);
      }
      if (arg === "--width") {
        width = value;
      } else {
        height = value;
      }
      continue;
    }
    if (arg.startsWith("-")) {
      throw new Error(`unknown option ${arg}\n${Usage}`);
    }
    positional.push(arg);
  }

  if (positional.length !== 2) {
    throw new Error(Usage);
  }
  const [url, out] = positional as [string, string];
  if (!/^https?:\/\//.test(url)) {
    throw new Error(`${url} is not an http:// or https:// URL`);
  }
  if (!out.endsWith(".png")) {
    throw new Error(`${out} must end in .png`);
  }
  return { url, out, width, height, fullPage };
}
