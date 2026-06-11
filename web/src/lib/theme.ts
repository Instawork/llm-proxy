import { useEffect, useState } from "react";

export type Theme = "light" | "dark";

const STORAGE_KEY = "llm-proxy-admin-theme";
const listeners = new Set<(t: Theme) => void>();

function initialTheme(): Theme {
  if (typeof window === "undefined") return "light";
  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (stored === "light" || stored === "dark") return stored;
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

let current: Theme = initialTheme();

/** Applies the current theme to <html data-theme> — call once at startup. */
export function initTheme() {
  if (typeof document !== "undefined") {
    document.documentElement.setAttribute("data-theme", current);
  }
}

export function getTheme(): Theme {
  return current;
}

export function setTheme(next: Theme) {
  current = next;
  if (typeof window !== "undefined") {
    window.localStorage.setItem(STORAGE_KEY, next);
    document.documentElement.setAttribute("data-theme", next);
  }
  listeners.forEach((l) => l(next));
}

/** Subscribes a component to the active theme; re-renders on change. */
export function useTheme(): Theme {
  const [theme, setLocal] = useState<Theme>(current);
  useEffect(() => {
    const listener = (t: Theme) => setLocal(t);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);
  return theme;
}

export function toggleTheme() {
  setTheme(current === "dark" ? "light" : "dark");
}
