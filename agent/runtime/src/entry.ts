import { realpathSync } from "node:fs";
import { fileURLToPath } from "node:url";

/**
 * isEntrypoint is whether the module at moduleURL is the program node was asked to run,
 * rather than a module a test or another entrypoint imported.
 *
 * It compares paths, not strings. import.meta.url is a percent-encoded URL and argv[1] is
 * a plain path, so `file://${argv[1]}` matches only a path that needs no escaping: a
 * runtime under a directory named `feature%2Fx`, or one with a space in it, never ran its
 * main, and an MCP server that exits without serving leaves a turn without its tools and
 * nothing to say why. Both sides are resolved through symlinks, because node runs the
 * real file under a linked path.
 */
export function isEntrypoint(moduleURL: string, argv1: string | undefined = process.argv[1]): boolean {
  if (argv1 === undefined || argv1 === "") {
    return false;
  }
  try {
    return realpathSync(fileURLToPath(moduleURL)) === realpathSync(argv1);
  } catch {
    return false;
  }
}
