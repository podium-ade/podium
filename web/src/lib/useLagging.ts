import { useEffect, useState } from "react";

/**
 * useLagging is `on`, but only once it has stayed on for `ms`. A read that answers quickly
 * then never shows that it was loading at all, so the screen does not blink on every change;
 * one that takes a while is shown as loading after a beat. It goes off the moment `on` does.
 */
export function useLagging(on: boolean, ms = 180): boolean {
  const [late, setLate] = useState(false);
  useEffect(() => {
    if (!on) return;
    const t = setTimeout(() => setLate(true), ms);
    return () => {
      clearTimeout(t);
      setLate(false);
    };
  }, [on, ms]);
  return on && late;
}

/**
 * dimmed is the one way a part of the screen says it is showing the previous answer while the
 * next one loads: a light fade, eased in and out, the same on every part of the screen.
 */
export function dimmed(stale: boolean | undefined): string {
  return stale ? "opacity-70 transition-opacity duration-200 ease-out" : "transition-opacity duration-200 ease-out";
}
