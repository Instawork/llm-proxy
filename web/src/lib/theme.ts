import { useEffect, useState } from "react";

export type Theme = "light" | "dark";
export type ThemePreference = "auto" | Theme;

const STORAGE_KEY = "llm-proxy-admin-theme";
const themeListeners = new Set<(t: Theme) => void>();
const preferenceListeners = new Set<(p: ThemePreference) => void>();

function systemTheme(): Theme {
  if (typeof window === "undefined") return "light";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function resolveTheme(preference: ThemePreference): Theme {
  if (preference === "auto") return systemTheme();
  return preference;
}

function initialPreference(): ThemePreference {
  if (typeof window === "undefined") return "auto";
  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (stored === "auto" || stored === "light" || stored === "dark") return stored;
  return "auto";
}

let currentPreference: ThemePreference = initialPreference();
let mediaQuery: MediaQueryList | null = null;

function notifyListeners() {
  const resolved = resolveTheme(currentPreference);
  themeListeners.forEach((listener) => listener(resolved));
  preferenceListeners.forEach((listener) => listener(currentPreference));
}

function applyTheme() {
  if (typeof document === "undefined") return;
  document.documentElement.setAttribute("data-theme", resolveTheme(currentPreference));
  notifyListeners();
}

function onSystemThemeChange() {
  if (currentPreference === "auto") {
    applyTheme();
  }
}

/** Applies the current theme to <html data-theme> — call once at startup. */
export function initTheme() {
  applyTheme();
  if (typeof window === "undefined") return;
  mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
  mediaQuery.addEventListener("change", onSystemThemeChange);
}

export function getTheme(): Theme {
  return resolveTheme(currentPreference);
}

export function getThemePreference(): ThemePreference {
  return currentPreference;
}

export function setThemePreference(next: ThemePreference) {
  currentPreference = next;
  if (typeof window !== "undefined") {
    window.localStorage.setItem(STORAGE_KEY, next);
  }
  applyTheme();
}

export function setThemeAuto(enabled: boolean) {
  if (enabled) {
    setThemePreference("auto");
    return;
  }
  setThemePreference(resolveTheme(currentPreference));
}

export function setTheme(next: Theme) {
  setThemePreference(next);
}

/** Subscribes a component to the resolved theme; re-renders on change. */
export function useTheme(): Theme {
  const [theme, setLocal] = useState<Theme>(resolveTheme(currentPreference));
  useEffect(() => {
    const listener = (t: Theme) => setLocal(t);
    themeListeners.add(listener);
    return () => {
      themeListeners.delete(listener);
    };
  }, []);
  return theme;
}

/** Subscribes a component to auto vs manual theme preference. */
export function useThemePreference(): ThemePreference {
  const [preference, setLocal] = useState<ThemePreference>(currentPreference);
  useEffect(() => {
    const listener = (p: ThemePreference) => setLocal(p);
    preferenceListeners.add(listener);
    return () => {
      preferenceListeners.delete(listener);
    };
  }, []);
  return preference;
}

export function toggleTheme() {
  setTheme(resolveTheme(currentPreference) === "dark" ? "light" : "dark");
}
