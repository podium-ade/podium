import { createContext, useCallback, useContext, useState } from "react";
import type { ReactNode } from "react";

const COLLAPSE_KEY = "podium-relay-sidebar-collapsed";

type ShellState = {
  navOpen: boolean;
  setNavOpen: (open: boolean) => void;
  collapsed: boolean;
  toggleCollapsed: () => void;
};

const ShellContext = createContext<ShellState | null>(null);

function readCollapsed() {
  try {
    return localStorage.getItem(COLLAPSE_KEY) === "true";
  } catch {
    return false;
  }
}

export function ShellStateProvider({ children }: { children: ReactNode }) {
  const [navOpen, setNavOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const toggleCollapsed = useCallback(() => {
    setCollapsed((value) => {
      const next = !value;
      try {
        localStorage.setItem(COLLAPSE_KEY, String(next));
      } catch {
        // The width still changes for this visit.
      }
      return next;
    });
  }, []);
  return (
    <ShellContext.Provider value={{ navOpen, setNavOpen, collapsed, toggleCollapsed }}>
      {children}
    </ShellContext.Provider>
  );
}

// The sidebar and the top bar both read this. It lives with the provider so they share one state.
// eslint-disable-next-line react-refresh/only-export-components
export function useShell(): ShellState {
  return (
    useContext(ShellContext) ?? {
      navOpen: false,
      setNavOpen: () => {},
      collapsed: false,
      toggleCollapsed: () => {},
    }
  );
}

