import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/** cn is the shadcn class helper: clsx for conditionals, twMerge so later utilities win. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
